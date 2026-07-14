package localserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cs-cloud/internal/logger"
)

// attachmentPathPattern matches the suffix `/api/v1/attachments/{id}` in
// any URL path — whether the request reaches this server via the local
// root (`http://host:port/api/v1/attachments/{id}`) or via the cloud
// device-proxy prefix (`http://host/cloud/device/{hash}/proxy/api/v1/attachments/{id}`).
// Captures the id so the rewriter can resolve it to an on-disk abs_path
// and rewrite the URL to a file:// URI when forwarding to a same-device
// agent (csc).
var attachmentPathPattern = regexp.MustCompile(`/api/v1/attachments/([^/?#]+)$`)

// LookupAttachmentAbsPath reads meta.json for the given attachment id and
// returns the stored absolute path. Returns ok=false when storage is
// unconfigured, the id is malformed, or the meta file is missing/corrupt.
//
// Used by the proxy URL rewriter to translate HTTP attachment URLs into
// file:// URIs before forwarding prompt bodies to a same-device csc.
func (s *Server) LookupAttachmentAbsPath(id string) (string, bool) {
	dir := s.attachmentsDir()
	if dir == "" {
		return "", false
	}
	storeDir := resolveAttachmentDir(dir, id)
	if storeDir == "" {
		return "", false
	}
	metaPath := filepath.Join(storeDir, "meta.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return "", false
	}
	var meta attachmentMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", false
	}
	if meta.AbsPath == "" {
		return "", false
	}
	if _, err := os.Stat(meta.AbsPath); err != nil {
		return "", false
	}
	return meta.AbsPath, true
}

// RewriteAttachmentURLs scans a prompt body for parts containing
// http(s) attachment URLs and rewrites each one to file://${absPath} when
// the referenced attachment lives on this device. Bodies that fail to
// parse as JSON, have no parts array, or contain no matching URLs are
// returned unchanged. This is the second half of the v2 contract:
// UI-facing wire format stays HTTP (so browsers can render previews),
// while the in-device agent receives file:// URIs that the csc compat
// layer turns into Anthropic image/document blocks without an HTTP fetch.
func (s *Server) RewriteAttachmentURLs(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		defer pw.Close()

		buf, err := io.ReadAll(body)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		trimmed := bytes.TrimSpace(buf)
		if len(trimmed) == 0 {
			_, _ = pw.Write(buf)
			return
		}

		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			_, _ = pw.Write(buf)
			return
		}

		partsVal, ok := payload["parts"]
		if !ok {
			_, _ = pw.Write(buf)
			return
		}
		parts, ok := partsVal.([]any)
		if !ok {
			_, _ = pw.Write(buf)
			return
		}

		rewritten := 0
		for i, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			// The wire format's "file" parts carry the URL under "url".
			// Some legacy clients may send "content" — skip those.
			urlVal, ok := part["url"]
			if !ok {
				continue
			}
			urlStr, ok := urlVal.(string)
			if !ok || urlStr == "" {
				continue
			}
			newURL, changed := s.translateAttachmentURL(urlStr)
			if changed {
				part["url"] = newURL
				parts[i] = part
				rewritten++
			}
		}

		if rewritten == 0 {
			_, _ = pw.Write(buf)
			return
		}

		payload["parts"] = parts
		encoded, err := json.Marshal(payload)
		if err != nil {
			logger.Warn("[attachment-proxy] failed to re-encode prompt body after URL rewrite: %v", err)
			_, _ = pw.Write(buf)
			return
		}
		logger.Info("[attachment-proxy] rewrote %d attachment URL(s) to file://", rewritten)
		_, _ = pw.Write(encoded)
	}()
	return pr
}

// translateAttachmentURL resolves an http(s) attachment URL to a file://
// URI when the referenced id exists in this server's storage. Returns the
// original URL and changed=false when no rewrite applies.
//
// Accepts both local (`http://host:port/api/v1/attachments/{id}`) and
// cloud device-proxy (`http://host/cloud/device/{hash}/proxy/api/v1/attachments/{id}`)
// URL shapes — the match is on the path suffix only.
func (s *Server) translateAttachmentURL(in string) (string, bool) {
	if !strings.HasPrefix(in, "http://") && !strings.HasPrefix(in, "https://") {
		return in, false
	}
	parsed, err := url.Parse(in)
	if err != nil {
		return in, false
	}
	m := attachmentPathPattern.FindStringSubmatch(parsed.Path)
	if m == nil {
		return in, false
	}
	id, err := url.PathUnescape(m[1])
	if err != nil {
		return in, false
	}
	absPath, ok := s.LookupAttachmentAbsPath(id)
	if !ok {
		return in, false
	}
	// file:// URI with absolute path. On Windows absPath begins with a
	// drive letter (e.g. C:\); node:url's fileURLToPath expects an extra
	// slash after file: for absolute POSIX paths, but for Windows paths
	// the form file:///C:/... is what node:url parses back to C:\...
	return "file:///" + filepath.ToSlash(absPath), true
}
