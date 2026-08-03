package localserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/logger"
)

// Attachment defaults — see docs/attachment-contract-v2.md §3.4 / §8.3.
const (
	attachmentMaxSize    = 10 << 20           // 10 MB per file
	attachmentTTL        = 7 * 24 * time.Hour // default retention
	attachmentSweepDeb   = 5 * time.Minute    // post-upload gc debounce window
)

// attachmentMeta is the on-disk metadata sidecar for each attachment.
// Stored as {attachmentsRoot}/{id}/meta.json.
type attachmentMeta struct {
	ID        string `json:"id"`
	AbsPath   string `json:"abs_path"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Sha256    string `json:"sha256"`
	Filename  string `json:"filename"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"` // unix seconds
}

func (s *Server) attachmentsDir() string {
	if s.rootDir == "" {
		return ""
	}
	return filepath.Join(s.rootDir, "attachments")
}

// generateAttachmentID returns a time-sortable hex identifier. Format:
//
//	{YYYYMMDDHHMMSS}-{16 hex random}
//
// Equivalent in spirit to ULID (time-sortable, no client input) without
// pulling in an external dependency. Contract v2 §3.2.
func generateAttachmentID() string {
	now := time.Now().UTC().Format("20060102150405")
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return now + "-" + hex.EncodeToString(b)
}

// extForMime maps a known-safe MIME to a lowercase extension so the on-disk
// filename carries its category in the path (lets agent FileReadTool route
// by extension). Unknown MIMEs get no extension — the agent falls back to
// the MIME field in the response.
func extForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

// allowedAttachmentMimes is the upload-side whitelist. Anything outside this
// set is rejected with 400 UNSUPPORTED_MIME. This gates the stored XSS vector
// at the door — without it an attacker can stash text/html or image/svg+xml
// and have it served back with that Content-Type via http.ServeFile.
var allowedAttachmentMimes = map[string]struct{}{
	"image/png":                {},
	"image/jpeg":               {},
	"image/jpg":                {},
	"image/gif":                {},
	"image/webp":               {},
	"application/pdf":          {},
	"application/octet-stream": {},
}

// isAllowedAttachmentMime reports whether mime is in the upload whitelist.
// Comparison is case-insensitive and trims surrounding whitespace, matching
// extForMime's normalization.
func isAllowedAttachmentMime(mime string) bool {
	_, ok := allowedAttachmentMimes[strings.ToLower(strings.TrimSpace(mime))]
	return ok
}

// sanitizeDispositionFilename returns a value safe to embed inside a
// Content-Disposition: attachment; filename="..." header. It strips control
// characters and the double-quote delimiter so the header cannot be broken
// out of. Empty / whitespace-only input yields "" so the caller can fall
// back to the attachment id.
func sanitizeDispositionFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	return out
}

// handleAttachmentUpload accepts a multipart file upload and stores it to disk.
// Contract v2 §I1: the response exposes abs_path as the only agent-facing
// reference handle. The internal id is for management only.
func (s *Server) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	dir := s.attachmentsDir()
	if dir == "" {
		writeErr(w, http.StatusServiceUnavailable, "NO_STORAGE", "attachment storage not configured")
		return
	}

	if err := r.ParseMultipartForm(attachmentMaxSize); err != nil {
		logger.Warn("[attachment] ParseMultipartForm error: %v (contentLength=%d)", err, r.ContentLength)
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "missing file field")
		return
	}
	defer file.Close()

	if header.Size > attachmentMaxSize {
		writeErr(w, http.StatusRequestEntityTooLarge, "TOO_LARGE",
			fmt.Sprintf("file size %d exceeds %d byte limit", header.Size, attachmentMaxSize))
		return
	}

	id := generateAttachmentID()
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	if !isAllowedAttachmentMime(mime) {
		writeErr(w, http.StatusBadRequest, "UNSUPPORTED_MIME",
			"attachment mime type not allowed; use a whitelisted image, pdf, or application/octet-stream")
		return
	}
	ext := extForMime(mime)

	storeDir := filepath.Join(dir, id)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", "failed to create storage directory")
		return
	}

	binPath := filepath.Join(storeDir, id+ext)
	dst, err := os.Create(binPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", "failed to create file")
		return
	}

	// Stream upload to disk while computing sha256 + total size in one pass.
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(dst, hasher), file)
	dst.Close()
	if err != nil {
		os.RemoveAll(storeDir)
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", "failed to write file")
		return
	}

	now := time.Now()
	expiresAt := now.Add(attachmentTTL)
	sha := hex.EncodeToString(hasher.Sum(nil))
	meta := attachmentMeta{
		ID:        id,
		AbsPath:   binPath,
		Mime:      mime,
		Size:      written,
		Sha256:    sha,
		Filename:  filepath.Base(header.Filename),
		CreatedAt: now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		os.RemoveAll(storeDir)
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", "failed to encode meta")
		return
	}
	if err := os.WriteFile(filepath.Join(storeDir, "meta.json"), metaBytes, 0o644); err != nil {
		os.RemoveAll(storeDir)
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", "failed to write meta")
		return
	}

	logger.Info("[attachment] uploaded id=%s size=%d mime=%s sha256=%s",
		id, written, mime, sha[:12])

	// Post-upload sweep: cheap lazy GC. Skipped if a sweep ran within the
	// debounce window, so burst uploads don't pay N× the scan cost. Runs
	// async — the upload response isn't blocked on cleanup.
	s.maybeSweepAttachments(dir)

	writeOK(w, map[string]any{
		"id":         meta.ID,
		"abs_path":   meta.AbsPath,
		"filename":   meta.Filename,
		"mime":       meta.Mime,
		"size":       meta.Size,
		"sha256":     meta.Sha256,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// resolveAttachmentDir safely maps an {id} path parameter to the on-disk
// attachment directory. Returns "" if id is malformed or escapes the root.
// Contract v2 §3.3 — path traversal defense.
func resolveAttachmentDir(dir, id string) string {
	if id == "" {
		return ""
	}
	// id must be a single path token, no separators, no parent escape.
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ""
	}
	candidate := filepath.Clean(filepath.Join(dir, id))
	root := filepath.Clean(dir) + string(os.PathSeparator)
	if candidate+string(os.PathSeparator) != root+id+string(os.PathSeparator) &&
		!strings.HasPrefix(candidate+string(os.PathSeparator), root) {
		return ""
	}
	// Final guard: ensure resolved path is still directly under root.
	rel, err := filepath.Rel(filepath.Clean(dir), candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	return candidate
}

// handleAttachmentGet serves a stored attachment's bytes for display.
// The {id} path parameter is the management id returned at upload time.
func (s *Server) handleAttachmentGet(w http.ResponseWriter, r *http.Request) {
	dir := s.attachmentsDir()
	if dir == "" {
		writeErr(w, http.StatusServiceUnavailable, "NO_STORAGE", "attachment storage not configured")
		return
	}

	id := r.PathValue("id")
	storeDir := resolveAttachmentDir(dir, id)
	if storeDir == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid id")
		return
	}

	metaPath := filepath.Join(storeDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "attachment not found")
		return
	}
	var meta attachmentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		writeErr(w, http.StatusInternalServerError, "CORRUPT", "meta unreadable")
		return
	}

	binPath := meta.AbsPath
	if binPath == "" {
		// Defensive fallback for older meta files.
		binPath = filepath.Join(storeDir, meta.ID+extForMime(meta.Mime))
	}
	if _, err := os.Stat(binPath); err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "attachment binary missing")
		return
	}

	// Defense-in-depth against stored XSS: never honor the stored meta.Mime
	// on the wire. Force a download with a generic Content-Type and pin
	// nosniff so the browser cannot reinterpret the bytes as HTML/SVG/etc.
	// http.ServeFile would otherwise preserve the Content-Type we set here
	// only when it cannot sniff one itself; setting it explicitly plus
	// attachment disposition is the canonical safe pattern.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if fname := sanitizeDispositionFilename(meta.Filename); fname != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+meta.ID+`"`)
	}
	if meta.Sha256 != "" {
		w.Header().Set("ETag", `"`+meta.Sha256+`"`)
	}
	http.ServeFile(w, r, binPath)
}

// handleAttachmentList returns summary stats for the attachment cache,
// including per-entry id / abs_path / size / mime / expires_at.
func (s *Server) handleAttachmentList(w http.ResponseWriter, r *http.Request) {
	dir := s.attachmentsDir()
	if dir == "" {
		writeErr(w, http.StatusServiceUnavailable, "NO_STORAGE", "attachment storage not configured")
		return
	}

	type entry struct {
		ID        string `json:"id"`
		AbsPath   string `json:"abs_path"`
		Size      int64  `json:"size"`
		Mime      string `json:"mime"`
		Filename  string `json:"filename"`
		ExpiresAt string `json:"expires_at"`
	}
	type stats struct {
		Directory string  `json:"directory"`
		TotalSize int64   `json:"total_size"`
		FileCount int     `json:"file_count"`
		Files     []entry `json:"files"`
	}

	entries, _ := os.ReadDir(dir)
	out := stats{Directory: dir}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(dir, e.Name(), "meta.json")
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m attachmentMeta
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		info, err := os.Stat(m.AbsPath)
		if err != nil {
			continue
		}
		out.Files = append(out.Files, entry{
			ID:        m.ID,
			AbsPath:   m.AbsPath,
			Size:      info.Size(),
			Mime:      m.Mime,
			Filename:  m.Filename,
			ExpiresAt: time.Unix(m.ExpiresAt, 0).UTC().Format(time.RFC3339),
		})
		out.TotalSize += info.Size()
		out.FileCount++
	}

	writeOK(w, out)
}

// handleAttachmentGC force-deletes all expired attachments. Triggered by
// the management route DELETE /attachments.
func (s *Server) handleAttachmentGC(w http.ResponseWriter, r *http.Request) {
	dir := s.attachmentsDir()
	if dir == "" {
		writeErr(w, http.StatusServiceUnavailable, "NO_STORAGE", "attachment storage not configured")
		return
	}

	deleted, freedBytes, err := GcExpiredAttachments(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "IO_ERROR", err.Error())
		return
	}

	logger.Info("[attachment] gc: deleted=%d freed=%d bytes", deleted, freedBytes)
	writeOK(w, map[string]any{
		"deleted_count": deleted,
		"freed_bytes":   freedBytes,
	})
}

// GcExpiredAttachments scans dir and removes every attachment whose meta.json
// reports ExpiresAt ≤ now. Returns the count of removed entries and the total
// bytes freed (sized from stat before removal). A missing directory is treated
// as "nothing to GC" (0/0, no error) so callers running independently of the
// daemon don't see spurious failures before any upload has happened. Other
// read-dir errors are reported via err; per-entry failures are logged and
// skipped so a single corrupted entry doesn't abort the sweep.
func GcExpiredAttachments(dir string) (deleted int, freedBytes int64, err error) {
	now := time.Now().Unix()

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, 0, nil
		}
		return 0, 0, readErr
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(dir, e.Name(), "meta.json")
		raw, ioErr := os.ReadFile(metaPath)
		if ioErr != nil {
			continue
		}
		var m attachmentMeta
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.ExpiresAt > now {
			continue
		}
		if info, statErr := os.Stat(m.AbsPath); statErr == nil {
			freedBytes += info.Size()
		}
		if rmErr := os.RemoveAll(filepath.Join(dir, e.Name())); rmErr != nil {
			logger.Warn("[attachment] failed to remove expired %s: %v", e.Name(), rmErr)
			continue
		}
		deleted++
	}
	return deleted, freedBytes, nil
}

// maybeSweepAttachments runs an async GC pass over the attachments directory,
// but only if one hasn't run within the attachmentSweepDeb window. This makes
// post-upload cleanup self-rate-limited: a burst of uploads pays the scan
// cost at most once per debounce window. The caller (upload handler) is never
// blocked — sweep runs in its own goroutine.
func (s *Server) maybeSweepAttachments(dir string) {
	if dir == "" {
		return
	}
	s.gcSweepMu.Lock()
	if time.Since(s.lastGcSweep) < attachmentSweepDeb {
		s.gcSweepMu.Unlock()
		return
	}
	s.lastGcSweep = time.Now()
	s.gcSweepMu.Unlock()

	go func() {
		deleted, _, err := GcExpiredAttachments(dir)
		if err != nil {
			return
		}
		if deleted > 0 {
			logger.Info("[attachment] post-upload sweep: removed %d expired entries", deleted)
		}
	}()
}
