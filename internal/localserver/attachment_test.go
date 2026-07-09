package localserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// io.Reader sanity (avoids unused import if not directly needed).
var _ = io.Discard
