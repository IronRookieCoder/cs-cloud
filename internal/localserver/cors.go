package localserver

import (
	"bufio"
	"net"
	"net/http"
)

// corsWriter wraps http.ResponseWriter to re-apply CORS headers
// right before WriteHeader or Write, ensuring they survive any
// header modifications by downstream handlers (e.g. ReverseProxy).
type corsWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *corsWriter) WriteHeader(code int) {
	if !w.wrote {
		setCORSHeaders(w.ResponseWriter.Header())
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *corsWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		setCORSHeaders(w.ResponseWriter.Header())
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

func setCORSHeaders(headers http.Header) {
	// Auth here is via the Authorization header, not cookies, so a cross-site
	// page cannot forge an authenticated request (the browser will not attach
	// Authorization automatically and cross-origin JS cannot read the victim's
	// stored token). A wildcard ACAO therefore exposes no credential-bearing
	// surface. This localserver is reached from both the local dev webview and
	// the cloud tunnel — whose browser-side Origin is the production domain —
	// so an Origin allowlist would only break legitimate traffic.
	headers.Set("Access-Control-Allow-Origin", "*")
	headers.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	headers.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Workspace-Directory, x-opencode-directory, x-csc-directory, Cookie")
	headers.Set("Access-Control-Max-Age", "86400")
	headers.Set("Vary", "Origin")
}

func stripCORSHeaders(headers http.Header) {
	headers.Del("Access-Control-Allow-Origin")
	headers.Del("Access-Control-Allow-Methods")
	headers.Del("Access-Control-Allow-Headers")
	headers.Del("Access-Control-Max-Age")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			setCORSHeaders(w.Header())
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Set headers early for visibility, but wrap with corsWriter
		// to re-apply them at WriteHeader/Write time, protecting against
		// downstream handlers (e.g. ReverseProxy copyHeader) overwriting them.
		setCORSHeaders(w.Header())
		next.ServeHTTP(&corsWriter{ResponseWriter: w}, r)
	})
}
