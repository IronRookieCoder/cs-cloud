package localserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer wires a Server with a temp rootDir for attachment tests.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	s := &Server{rootDir: root}
	return s, root
}

func writeMultipart(t *testing.T, field, filename, mime string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreatePart(textprotoHeader(mime, filename))
	if err != nil {
		t.Fatalf("create form part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// textprotoHeader builds the MIME header for a multipart file part that
// mimics what http.MultipartWriter.CreateFormFile would produce but with
// a caller-supplied Content-Type.
func textprotoHeader(mime, filename string) map[string][]string {
	return map[string][]string{
		"Content-Disposition": {"form-data; name=\"file\"; filename=\"" + filename + "\""},
		"Content-Type":        {mime},
	}
}

func TestAttachmentUploadReturnsAbsolutePathAndSHA256(t *testing.T) {
	s, root := newTestServer(t)
	content := []byte("hello attachment world")
	body, ct := writeMultipart(t, "file", "note.txt", "text/plain", content)

	req := httptest.NewRequest("POST", "/api/v1/attachments", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleAttachmentUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := json.Marshal(resp.Data)
	var got struct {
		ID        string `json:"id"`
		AbsPath   string `json:"abs_path"`
		Mime      string `json:"mime"`
		Size      int64  `json:"size"`
		Sha256    string `json:"sha256"`
		Filename  string `json:"filename"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	if got.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", got.Size, len(content))
	}
	if got.Mime != "text/plain" {
		t.Errorf("mime = %q, want text/plain", got.Mime)
	}
	if got.Filename != "note.txt" {
		t.Errorf("filename = %q, want note.txt", got.Filename)
	}
	if got.Sha256 == "" {
		t.Fatalf("sha256 missing")
	}
	expect := sha256.Sum256(content)
	if got.Sha256 != hex.EncodeToString(expect[:]) {
		t.Errorf("sha256 = %q, want %x", got.Sha256, expect)
	}
	if got.ExpiresAt == "" {
		t.Fatalf("expires_at missing")
	}
	// abs_path must be platform-native and live under the root.
	if !strings.HasPrefix(filepath.Clean(got.AbsPath), filepath.Clean(root)+string(os.PathSeparator)) {
		t.Errorf("abs_path %q is not under root %q", got.AbsPath, root)
	}
	if _, err := os.Stat(got.AbsPath); err != nil {
		t.Errorf("abs_path file does not exist: %v", err)
	}
	// id must not contain path separators (used as a single-segment path param).
	if strings.ContainsAny(got.ID, `/\`) {
		t.Errorf("id %q contains a path separator", got.ID)
	}
}

func TestAttachmentUploadRejectsOversizeFile(t *testing.T) {
	s, _ := newTestServer(t)
	// attachmentMaxSize is 10 << 20; force header.Size to exceed it by
	// writing a body slightly larger.
	big := bytes.Repeat([]byte{'x'}, attachmentMaxSize+1)
	body, ct := writeMultipart(t, "file", "big.bin", "application/octet-stream", big)

	req := httptest.NewRequest("POST", "/api/v1/attachments", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleAttachmentUpload(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAttachmentGetServesStoredBytes(t *testing.T) {
	s, _ := newTestServer(t)
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	body, ct := writeMultipart(t, "file", "pixel.png", "image/png", png)

	upReq := httptest.NewRequest("POST", "/api/v1/attachments", body)
	upReq.Header.Set("Content-Type", ct)
	upRec := httptest.NewRecorder()
	s.handleAttachmentUpload(upRec, upReq)
	if upRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", upRec.Code, upRec.Body.String())
	}

	var up envelope
	json.Unmarshal(upRec.Body.Bytes(), &up)
	dataBytes, _ := json.Marshal(up.Data)
	var got struct {
		ID string `json:"id"`
	}
	json.Unmarshal(dataBytes, &got)

	getReq := httptest.NewRequest("GET", "/api/v1/attachments/"+got.ID, nil)
	getReq.SetPathValue("id", got.ID)
	getRec := httptest.NewRecorder()
	s.handleAttachmentGet(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	if getRec.Body.Bytes() == nil {
		t.Fatalf("get body empty")
	}
	if !bytes.Equal(getRec.Body.Bytes(), png) {
		t.Errorf("get body mismatch: got %d bytes, want %d", len(getRec.Body.Bytes()), len(png))
	}
	if ct := getRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

func TestAttachmentGetRejectsTraversalID(t *testing.T) {
	s, _ := newTestServer(t)
	for _, bad := range []string{"..", "../", ".." + string(os.PathSeparator) + "secret", "foo/bar", "foo\\bar"} {
		req := httptest.NewRequest("GET", "/api/v1/attachments/"+bad, nil)
		req.SetPathValue("id", bad)
		rec := httptest.NewRecorder()
		s.handleAttachmentGet(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("id %q should not have served (status 200, body=%q)", bad, rec.Body.String())
		}
	}
}

func TestAttachmentListReportsEntries(t *testing.T) {
	s, _ := newTestServer(t)
	// Upload two attachments.
	for _, name := range []string{"a.png", "b.png"} {
		body, ct := writeMultipart(t, "file", name, "image/png", []byte{0x89, 'P', 'N', 'G'})
		req := httptest.NewRequest("POST", "/api/v1/attachments", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		s.handleAttachmentUpload(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %s status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/attachments", nil)
	rec := httptest.NewRecorder()
	s.handleAttachmentList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp envelope
	json.Unmarshal(rec.Body.Bytes(), &resp)
	dataBytes, _ := json.Marshal(resp.Data)
	var got struct {
		TotalSize int64 `json:"total_size"`
		FileCount int   `json:"file_count"`
	}
	json.Unmarshal(dataBytes, &got)
	if got.FileCount != 2 {
		t.Errorf("file_count = %d, want 2", got.FileCount)
	}
	if got.TotalSize == 0 {
		t.Errorf("total_size = 0, want > 0")
	}
}

func TestAttachmentGCRemovesExpired(t *testing.T) {
	s, root := newTestServer(t)
	body, ct := writeMultipart(t, "file", "x.png", "image/png", []byte{0x89, 'P', 'N', 'G'})
	upReq := httptest.NewRequest("POST", "/api/v1/attachments", body)
	upReq.Header.Set("Content-Type", ct)
	upRec := httptest.NewRecorder()
	s.handleAttachmentUpload(upRec, upReq)
	if upRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", upRec.Code)
	}
	var up envelope
	json.Unmarshal(upRec.Body.Bytes(), &up)
	dataBytes, _ := json.Marshal(up.Data)
	var meta struct {
		ID string `json:"id"`
	}
	json.Unmarshal(dataBytes, &meta)

	// Forcibly expire the meta.json on disk.
	metaPath := filepath.Join(root, "attachments", meta.ID, "meta.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m attachmentMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	m.ExpiresAt = 1 // ancient
	out, _ := json.Marshal(m)
	if err := os.WriteFile(metaPath, out, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/attachments", nil)
	rec := httptest.NewRecorder()
	s.handleAttachmentGC(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gc status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var gcResp envelope
	json.Unmarshal(rec.Body.Bytes(), &gcResp)
	gcData, _ := json.Marshal(gcResp.Data)
	var got struct {
		DeletedCount int `json:"deleted_count"`
	}
	json.Unmarshal(gcData, &got)
	if got.DeletedCount < 1 {
		t.Errorf("deleted_count = %d, want >= 1", got.DeletedCount)
	}
	// The id dir should be gone.
	if _, err := os.Stat(filepath.Join(root, "attachments", meta.ID)); err == nil {
		t.Errorf("attachment dir still present after gc")
	}
}

func TestAttachmentUploadReturnsErrorWhenStorageUnconfigured(t *testing.T) {
	s := &Server{} // rootDir intentionally empty
	body, ct := writeMultipart(t, "file", "x.png", "image/png", []byte{0x89})
	req := httptest.NewRequest("POST", "/api/v1/attachments", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleAttachmentUpload(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// writeAttachmentOnDisk creates an attachment directory with meta.json and a
// binary file. If expiresAt <= 0, the meta reports ExpiresAt = 1 (already
// expired); otherwise it reports ExpiresAt = now + expiresAt.
func writeAttachmentOnDisk(t *testing.T, root, id string, payload []byte, expiresAt int64) string {
	t.Helper()
	storeDir := filepath.Join(root, "attachments", id)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", storeDir, err)
	}
	binPath := filepath.Join(storeDir, id+".bin")
	if err := os.WriteFile(binPath, payload, 0o644); err != nil {
		t.Fatalf("write bin %s: %v", binPath, err)
	}
	var exp int64
	if expiresAt <= 0 {
		exp = 1
	} else {
		exp = time.Now().Unix() + expiresAt
	}
	meta := attachmentMeta{
		ID:        id,
		AbsPath:   binPath,
		Mime:      "application/octet-stream",
		Size:      int64(len(payload)),
		Sha256:    "",
		Filename:  id + ".bin",
		CreatedAt: time.Now().Unix(),
		ExpiresAt: exp,
	}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(storeDir, "meta.json"), raw, 0o644); err != nil {
		t.Fatalf("write meta %s: %v", storeDir, err)
	}
	return binPath
}

// TestGcExpiredAttachmentsMissingDirReturnsOK covers the "nothing to GC yet"
// contract: when the attachments directory doesn't exist (e.g. CLI gc run
// before any upload), the function returns 0/0/nil rather than an error.
func TestGcExpiredAttachmentsMissingDirReturnsOK(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "attachments", "does-not-exist")
	deleted, freed, err := GcExpiredAttachments(missing)
	if err != nil {
		t.Fatalf("err = %v; want nil for missing dir", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d; want 0", deleted)
	}
	if freed != 0 {
		t.Errorf("freed = %d; want 0", freed)
	}
}

// TestGcExpiredAttachmentsPreservesFreshAndRemovesExpired mixes one expired
// and one fresh entry in the same directory. Only the expired one should be
// removed; the fresh one must survive and contribute nothing to freed bytes.
func TestGcExpiredAttachmentsPreservesFreshAndRemovesExpired(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	expiredBytes := []byte("expired-payload-xxxx")
	freshBytes := []byte("fresh-payload-yyyy")
	writeAttachmentOnDisk(t, root, "expired-1", expiredBytes, 0)        // 0 → expired
	writeAttachmentOnDisk(t, root, "fresh-1", freshBytes, 60*60)        // +1h fresh

	deleted, freed, err := GcExpiredAttachments(dir)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d; want 1", deleted)
	}
	if freed != int64(len(expiredBytes)) {
		t.Errorf("freed = %d; want %d", freed, len(expiredBytes))
	}
	if _, err := os.Stat(filepath.Join(dir, "expired-1")); err == nil {
		t.Errorf("expired-1 dir still present after gc")
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh-1")); err != nil {
		t.Errorf("fresh-1 dir removed by gc; should have been preserved: %v", err)
	}
}

// TestGcExpiredAttachmentsSkipsCorruptMeta verifies a single bad entry
// (missing meta.json, malformed JSON) does not abort the sweep — other
// expired entries are still cleaned up.
func TestGcExpiredAttachmentsSkipsCorruptMeta(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Good expired entry — should be removed.
	writeAttachmentOnDisk(t, root, "good-expired", []byte("good"), 0)

	// Entry with no meta.json — must be skipped silently.
	noMetaDir := filepath.Join(dir, "no-meta")
	if err := os.MkdirAll(noMetaDir, 0o755); err != nil {
		t.Fatalf("mkdir no-meta: %v", err)
	}

	// Entry with malformed JSON meta — must be skipped silently.
	badDir := filepath.Join(dir, "bad-meta")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir bad-meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write bad meta: %v", err)
	}

	deleted, _, err := GcExpiredAttachments(dir)
	if err != nil {
		t.Fatalf("err = %v; want nil even with corrupt entries", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d; want 1 (only the good expired entry)", deleted)
	}
	if _, err := os.Stat(noMetaDir); err != nil {
		t.Errorf("no-meta dir state changed unexpectedly: %v", err)
	}
	if _, err := os.Stat(badDir); err != nil {
		t.Errorf("bad-meta dir state changed unexpectedly: %v", err)
	}
}

// TestGcExpiredAttachmentsIgnoresNonDirectoryEntries confirms stray files
// at the attachments root (e.g. .DS_Store, leftovers) are ignored, not
// treated as expired entries.
func TestGcExpiredAttachmentsIgnoresNonDirectoryEntries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Stray file at root level — must not crash, must not be counted.
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	deleted, _, err := GcExpiredAttachments(dir)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d; want 0 (stray file should not be treated as entry)", deleted)
	}
}

// TestMaybeSweepAttachmentsEmptyDirIsNoOp confirms that passing "" short-
// circuits without touching lastGcSweep (so the next real call still runs).
func TestMaybeSweepAttachmentsEmptyDirIsNoOp(t *testing.T) {
	s, _ := newTestServer(t)
	s.maybeSweepAttachments("")
	if !s.lastGcSweep.IsZero() {
		t.Errorf("lastGcSweep = %v; want zero (empty dir should not stamp)", s.lastGcSweep)
	}
}

// TestMaybeSweepAttachmentsDebouncesSecondCallWithinWindow covers the
// debounce contract: a second invocation within the debounce window must
// not stamp lastGcSweep again, so bursty uploads pay at most one sweep.
func TestMaybeSweepAttachmentsDebouncesSecondCallWithinWindow(t *testing.T) {
	s, root := newTestServer(t)
	dir := filepath.Join(root, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s.maybeSweepAttachments(dir)
	first := s.lastGcSweep
	if first.IsZero() {
		t.Fatalf("first call did not stamp lastGcSweep")
	}

	// Simulate a call inside the debounce window by rewinding the stamp
	// slightly so we don't depend on real-time tick granularity.
	s.gcSweepMu.Lock()
	s.lastGcSweep = time.Now().Add(-attachmentSweepDeb / 2)
	s.gcSweepMu.Unlock()

	s.maybeSweepAttachments(dir)
	if !s.lastGcSweep.Equal(time.Now().Add(-attachmentSweepDeb / 2)) {
		t.Errorf("lastGcSweep changed inside debounce window; debounce failed")
	}
}

// TestMaybeSweepAttachmentsRunsAfterWindowExpires confirms the debounce
// window eventually reopens. We pre-stamp lastGcSweep far enough in the past
// to verify the next call restamps.
func TestMaybeSweepAttachmentsRunsAfterWindowExpires(t *testing.T) {
	s, root := newTestServer(t)
	dir := filepath.Join(root, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s.gcSweepMu.Lock()
	s.lastGcSweep = time.Now().Add(-attachmentSweepDeb - time.Second)
	s.gcSweepMu.Unlock()

	s.maybeSweepAttachments(dir)
	oldStamp := time.Now().Add(-attachmentSweepDeb - time.Second)
	if !s.lastGcSweep.After(oldStamp) {
		t.Errorf("lastGcSweep not updated after debounce window expired")
	}
}
