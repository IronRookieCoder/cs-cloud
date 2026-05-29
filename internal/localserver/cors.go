package localserver

import (
	"bufio"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// corsWriter wraps http.ResponseWriter to re-apply CORS headers
// right before WriteHeader or Write, ensuring they survive any
// header modifications by downstream handlers (e.g. ReverseProxy).
type corsWriter struct {
	http.ResponseWriter
	origin string
	wrote  bool
}

func (w *corsWriter) WriteHeader(code int) {
	if !w.wrote {
		setCORSHeaders(w.ResponseWriter.Header(), w.origin)
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *corsWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		setCORSHeaders(w.ResponseWriter.Header(), w.origin)
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher so that downstream handlers relying on
// streaming (e.g. ReverseProxy with FlushInterval for SSE /events) can
// flush buffered data to the client.
func (w *corsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *corsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func setCORSHeaders(headers http.Header, origin string) {
	if origin == "" {
		origin = "*"
	}

	// When the request origin is a localhost variant (127.0.0.1 or localhost),
	// VS Code's service worker may rewrite the Origin header when proxying
	// webview requests (e.g., page origin http://localhost:8282 becomes
	// http://127.0.0.1 in the proxied request). The browser then rejects the
	// CORS response because the echoed origin doesn't match the page origin.
	//
	// For localhost origins, use a wildcard and skip credentials. Auth is
	// handled via the Authorization header, not cookies, so dropping
	// Access-Control-Allow-Credentials is safe.
	if isLocalhostOrigin(origin) {
		headers.Set("Access-Control-Allow-Origin", "*")
	} else {
		headers.Set("Access-Control-Allow-Origin", origin)
		headers.Set("Access-Control-Allow-Credentials", "true")
	}

	headers.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	headers.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Workspace-Directory, x-opencode-directory, x-csc-directory, Cookie")
	headers.Set("Access-Control-Max-Age", "86400")
	headers.Set("Vary", "Origin")
}

// isLocalhostOrigin reports whether the origin is a localhost variant
// (http://localhost, http://127.0.0.1, or http://[::1] on any port).
func isLocalhostOrigin(origin string) bool {
	if origin == "" || origin == "*" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func stripCORSHeaders(headers http.Header) {
	headers.Del("Access-Control-Allow-Origin")
	headers.Del("Access-Control-Allow-Methods")
	headers.Del("Access-Control-Allow-Headers")
	headers.Del("Access-Control-Max-Age")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if r.Method == http.MethodOptions {
			setCORSHeaders(w.Header(), origin)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Set headers early for visibility, but wrap with corsWriter
		// to re-apply them at WriteHeader/Write time, protecting against
		// downstream handlers (e.g. ReverseProxy copyHeader) overwriting them.
		setCORSHeaders(w.Header(), origin)
		next.ServeHTTP(&corsWriter{ResponseWriter: w, origin: origin}, r)
	})
}
