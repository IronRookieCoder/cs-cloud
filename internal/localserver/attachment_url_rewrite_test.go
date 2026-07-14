package localserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLookupAttachmentAbsPathResolvesStoredMeta exercises the full path:
// upload an attachment, then look up its abs_path by id.
func TestLookupAttachmentAbsPathResolvesStoredMeta(t *testing.T) {
	s, _ := newTestServer(t)
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	body, ct := writeMultipart(t, "file", "pixel.png", "image/png", png)
	req := httptest.NewRequest("POST", "/api/v1/attachments", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleAttachmentUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var up envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dataBytes, _ := json.Marshal(up.Data)
	var got struct {
		ID      string `json:"id"`
		AbsPath string `json:"abs_path"`
	}
	if err := json.Unmarshal(dataBytes, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	got2, ok := s.LookupAttachmentAbsPath(got.ID)
	if !ok {
		t.Fatalf("LookupAttachmentAbsPath(%q) returned ok=false; want true", got.ID)
	}
	if got2 != got.AbsPath {
		t.Errorf("LookupAttachmentAbsPath = %q; want %q", got2, got.AbsPath)
	}
}

func TestLookupAttachmentAbsPathRejectsUnknownID(t *testing.T) {
	s, _ := newTestServer(t)
	if _, ok := s.LookupAttachmentAbsPath("does-not-exist"); ok {
		t.Errorf("expected ok=false for unknown id")
	}
	// Path traversal attempts must not crash or succeed.
	for _, bad := range []string{"..", "../secret", "foo/bar"} {
		if _, ok := s.LookupAttachmentAbsPath(bad); ok {
			t.Errorf("LookupAttachmentAbsPath(%q) returned ok=true; want false", bad)
		}
	}
}

// TestRewriteAttachmentURLsRewritesMatchingParts verifies that an
// http attachment URL pointing at this server's storage is rewritten to
// file:///... in the parts array before forwarding.
func TestRewriteAttachmentURLsRewritesMatchingParts(t *testing.T) {
	s, _ := newTestServer(t)
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	body, ct := writeMultipart(t, "file", "pixel.png", "image/png", png)
	req := httptest.NewRequest("POST", "/api/v1/attachments", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleAttachmentUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", rec.Code)
	}
	var up envelope
	json.Unmarshal(rec.Body.Bytes(), &up)
	dataBytes, _ := json.Marshal(up.Data)
	var upData struct {
		ID string `json:"id"`
	}
	json.Unmarshal(dataBytes, &upData)

	cases := []struct {
		name  string
		inURL string
	}{
		{
			name:  "local root URL",
			inURL: "http://127.0.0.1:8080/api/v1/attachments/" + upData.ID,
		},
		{
			name:  "cloud device-proxy URL",
			inURL: "http://127.0.0.1:3000/cloud/device/c29a06551f71e5713ed28d912877ed54af3f2f7afd24e80dab4df4e7f79d878d/proxy/api/v1/attachments/" + upData.ID,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := map[string]any{
				"parts": []any{
					map[string]any{"type": "text", "text": "look at this"},
					map[string]any{"type": "file", "url": c.inURL, "mime": "image/png"},
				},
			}
			encoded, _ := json.Marshal(payload)

			out := s.RewriteAttachmentURLs(io.NopCloser(strings.NewReader(string(encoded))))
			defer out.Close()
			got, err := io.ReadAll(out)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			var resp map[string]any
			if err := json.Unmarshal(got, &resp); err != nil {
				t.Fatalf("unmarshal output: %v (body=%s)", err, got)
			}
			parts := resp["parts"].([]any)
			filePart := parts[1].(map[string]any)
			url := filePart["url"].(string)
			if !strings.HasPrefix(url, "file:///") {
				t.Errorf("url = %q; want file:///...", url)
			}
		})
	}
}

// TestRewriteAttachmentURLsIgnoresUnknownAttachmentID confirms the
// rewriter leaves URLs intact when the id does not resolve (e.g. an
// attachment hosted on a different device).
func TestRewriteAttachmentURLsIgnoresUnknownAttachmentID(t *testing.T) {
	s, _ := newTestServer(t)
	inURL := "http://127.0.0.1:8080/api/v1/attachments/foreign-id"
	payload := map[string]any{
		"parts": []any{
			map[string]any{"type": "file", "url": inURL, "mime": "image/png"},
		},
	}
	encoded, _ := json.Marshal(payload)

	out := s.RewriteAttachmentURLs(io.NopCloser(strings.NewReader(string(encoded))))
	defer out.Close()
	got, _ := io.ReadAll(out)

	var resp map[string]any
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	parts := resp["parts"].([]any)
	filePart := parts[0].(map[string]any)
	url := filePart["url"].(string)
	if url != inURL {
		t.Errorf("url = %q; want unchanged %q", url, inURL)
	}
}

// TestRewriteAttachmentURLsLeavesNonJSONBodyUnchanged ensures the
// rewriter is a no-op for bodies that are not the prompt JSON shape.
func TestRewriteAttachmentURLsLeavesNonJSONBodyUnchanged(t *testing.T) {
	s, _ := newTestServer(t)
	raw := `not-json-at-all`

	out := s.RewriteAttachmentURLs(io.NopCloser(strings.NewReader(raw)))
	defer out.Close()
	got, _ := io.ReadAll(out)
	if string(got) != raw {
		t.Errorf("non-JSON body changed: got=%q want=%q", got, raw)
	}
}

// TestIsPromptRoute covers the route gate used by handleProxy.
func TestIsPromptRoute(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/conversations/abc/prompt", true},
		{"/conversations/abc/prompt/async", true},
		{"/conversations/abc/messages", false},
		{"/conversations", false},
		{"/permissions", false},
		{"/attachments", false},
	}
	for _, c := range cases {
		if got := isPromptRoute(c.path); got != c.want {
			t.Errorf("isPromptRoute(%q) = %v; want %v", c.path, got, c.want)
		}
	}
}
