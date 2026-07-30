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
	// Non-localhost Origin: refuse cross-origin access. This is a local dev
	// server bound to 127.0.0.1 and only localhost-variant webview/webapp
	// origins are trusted. Reflecting arbitrary origins (the old behavior)
	// let any malicious page the victim visits issue cross-origin requests
	// against their localserver and read the responses, exposing every API
	// endpoint (file read/write, terminal, workflow). Setting no CORS
	// headers makes the browser block the response from being read.
	if origin != "" && !isLocalhostOrigin(origin) {
		return
	}

	// No Origin header (non-browser clients like curl) or localhost origin:
	// wildcard ACAO. VS Code's service worker may rewrite the Origin header
	// when proxying webview requests (page origin http://localhost:8282
	// becomes http://127.0.0.1 in the proxied request), so a wildcard avoids
	// a browser-side ACAO/page-origin mismatch. Auth is via the
	// Authorization header, not cookies, so dropping
	// Access-Control-Allow-Credentials is safe.
	headers.Set("Access-Control-Allow-Origin", "*")
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
