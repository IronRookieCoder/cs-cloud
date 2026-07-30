package localserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/logger"
)

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	endpoint := s.manager.Endpoint()
	if endpoint == "" {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no agent backend available")
		return
	}

	targetURL, err := url.Parse(endpoint)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "invalid backend endpoint")
		return
	}

	backend := s.manager.DefaultBackend()
	d, ok := s.manager.GetDriver(backend)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no driver for backend: "+backend)
		return
	}

	var rewriteFunc func(map[string]string) string
	var transformFunc func(io.ReadCloser) io.ReadCloser
	cleanPath := strings.TrimPrefix(r.URL.Path, "/api/v1")
	for _, rt := range d.ProxyRoutes() {
		if r.Method != rt.Method {
			continue
		}
		if matchRoute(cleanPath, rt.Prefix) {
			rewriteFunc = rt.Rewrite
			transformFunc = rt.Transform
			break
		}
	}

	if rewriteFunc == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "no proxy route for "+r.URL.Path)
		return
	}

	pathValues := extractPathValues(r)
	target := rewriteFunc(pathValues)

	// Track session→workspace for routes with {id} in path
	if sessionID := pathValues["id"]; sessionID != "" {
		if workspace := getWorkspaceDir(r); workspace != "" {
			if abs, err := filepath.Abs(filepath.Clean(workspace)); err == nil {
				s.eventBus.RegisterSessionCwd(sessionID, abs)
			}
		}
	}

	if transformFunc != nil && r.Body != nil {
		r.Body = transformFunc(r.Body)
		// Prompt routes carry attachment URLs in the wire format
		// (`{baseUrl}/api/v1/attachments/{id}`). When forwarding to a
		// same-device agent, rewrite those URLs to file://${absPath} so
		// csc's compat layer can stream bytes from disk instead of
		// trying to fetch a cloud-proxy URL it has no auth for.
		if isPromptRoute(cleanPath) {
			r.Body = s.RewriteAttachmentURLs(r.Body)
		}
		// 设置为未知长度，让 ReverseProxy 流式处理
		r.ContentLength = -1
		r.Header.Set("Transfer-Encoding", "chunked")
	}

	targetAddr := targetURL.Scheme + "://" + targetURL.Host + target
	start := time.Now()

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.FlushInterval = -1

	headerMap := d.HeaderMap()

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = target
		req.URL.RawPath = ""
		req.Host = targetURL.Host
		for from, to := range headerMap {
			if v := req.Header.Get(from); v != "" {
				req.Header.Set(to, v)
			}
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("proxy %s %s -> %s %d %s err: %v", r.Method, r.URL.Path, targetAddr, http.StatusBadGateway, time.Since(start), err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	isConversationCreate := r.Method == http.MethodPost && cleanPath == "/conversations"

	// Auto-create the working directory for conversation creation requests.
	// csc's sessionManager rejects non-existent cwd with "Working directory
	// does not exist"; mkdir -p here so callers can pass a fresh path per
	// session without pre-provisioning it.
	if isConversationCreate {
		ensureConversationWorkdir(r)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		stripCORSHeaders(resp.Header)

		// Track session→workspace for conversation creation responses
		if resp.StatusCode < 400 && isConversationCreate {
			body, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				var result map[string]any
				if json.Unmarshal(body, &result) == nil {
					if id, ok := result["id"].(string); ok && id != "" {
						if workspace := getWorkspaceDir(r); workspace != "" {
							if abs, absErr := filepath.Abs(filepath.Clean(workspace)); absErr == nil {
								s.eventBus.RegisterSessionCwd(id, abs)
							}
						}
					}
				}
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
			logger.Info("proxy %s %s -> %s %d %s", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start))
			return nil
		}

		if resp.StatusCode >= 400 {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				logger.Error("proxy %s %s -> %s %d %s, failed to read response body: %v", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start), readErr)
				return nil
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			logger.Error("proxy %s %s -> %s %d %s, body: %s", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start), body)
		}
		return nil
	}

	proxy.ServeHTTP(w, r)
}

// ensureConversationWorkdir makes sure the conversation's working directory
// exists before the request is forwarded to csc. csc's sessionManager rejects
// non-existent cwd with "Working directory does not exist", so mkdir -p here
// lets callers pass a fresh path per session without pre-provisioning it.
//
// Resolution order:
//  1. `X-Workspace-Directory` header (canonical mechanism, URL-decoded)
//  2. body `cwd` JSON field (fallback for callers that use it)
//
// Errors are logged only — mkdir failure still lets csc surface its own
// validation error. The request body is always restored verbatim.
func ensureConversationWorkdir(r *http.Request) {
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			logger.Warn("auto-mkdir: read body failed: %v", err)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	cwd := getWorkspaceDir(r)
	if cwd == "" && len(body) > 0 {
		var payload struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(body, &payload); err == nil {
			cwd = payload.Cwd
		}
	}
	if cwd == "" {
		return
	}

	abs, err := filepath.Abs(cwd)
	if err != nil {
		logger.Warn("auto-mkdir: resolve %q failed: %v", cwd, err)
		return
	}
	if info, err := os.Stat(abs); err == nil {
		if !info.IsDir() {
			logger.Warn("auto-mkdir: %s exists and is not a directory", abs)
		}
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		logger.Warn("auto-mkdir: create %s failed: %v", abs, err)
		return
	}
	logger.Info("auto-mkdir: created %s", abs)
}

func extractPathValues(r *http.Request) map[string]string {
	vals := make(map[string]string)
	for _, key := range []string{"id"} {
		if v := r.PathValue(key); v != "" {
			vals[key] = v
		}
	}
	return vals
}

// isPromptRoute reports whether cleanPath targets the prompt endpoint
// (sync or async). Used to gate the attachment-URL rewrite pass to the
// only routes whose bodies carry attachment references.
func isPromptRoute(cleanPath string) bool {
	if !strings.HasPrefix(cleanPath, "/conversations/") {
		return false
	}
	return strings.HasSuffix(cleanPath, "/prompt") || strings.HasSuffix(cleanPath, "/prompt/async")
}

func matchRoute(path, pattern string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")

	if len(pathParts) < len(patternParts) {
		return false
	}

	for i, pp := range patternParts {
		if strings.HasPrefix(pp, "{") && strings.HasSuffix(pp, "}") {
			continue
		}
		if pp != pathParts[i] {
			return false
		}
	}

	if len(patternParts) > 0 && !strings.Contains(patternParts[len(patternParts)-1], "{") &&
		len(pathParts) > len(patternParts) {
		return false
	}

	return len(pathParts) == len(patternParts)
}
