package localserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/config"
)

func TestAuthMiddleware_DisabledWhenNoKey(t *testing.T) {
	called := false
	h := authMiddleware("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil))

	if !called {
		t.Fatal("downstream handler should be called when no API key is configured")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RequiresKey(t *testing.T) {
	const key = "s3cret-key"
	h := authMiddleware(key)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name   string
		header string // "Authorization" or "X-API-Key" via setup below
		set    func(req *http.Request)
		want   int
	}{
		{name: "no header", set: func(*http.Request) {}, want: http.StatusUnauthorized},
		{name: "wrong bearer", set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, want: http.StatusUnauthorized},
		{name: "correct bearer", set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }, want: http.StatusOK},
		{name: "correct x-api-key", set: func(r *http.Request) { r.Header.Set("X-API-Key", key) }, want: http.StatusOK},
		{name: "wrong x-api-key", set: func(r *http.Request) { r.Header.Set("X-API-Key", "nope") }, want: http.StatusUnauthorized},
		{name: "non-bearer scheme", set: func(r *http.Request) { r.Header.Set("Authorization", "Basic "+key) }, want: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
			tc.set(req)
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d (body=%q)", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestServer_NoAPIKeyConfigured_PassesThrough exercises the full wiring in
// New(): when Config.APIKey is empty the auth middleware must be a no-op so
// existing unauthenticated clients keep working.
func TestServer_NoAPIKeyConfigured_PassesThrough(t *testing.T) {
	srv := New(WithVersion("test"), WithConfig(&config.Config{}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("got 401 on default (no-key) server; auth middleware should be a no-op")
	}
}

// TestServer_APIKeyEnforced_EndToEnd verifies the wired chain
// (corsMiddleware → authMiddleware → handler) rejects requests without the
// key and accepts requests that present it correctly.
func TestServer_APIKeyEnforced_EndToEnd(t *testing.T) {
	const key = "topsecret"
	srv := New(WithVersion("test"), WithConfig(&config.Config{APIKey: key}))

	t.Run("rejects without key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("WWW-Authenticate"); got == "" {
			t.Errorf("expected WWW-Authenticate header on 401 response")
		}
	})

	t.Run("accepts with bearer", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("got 401 despite valid Bearer; body=%s", rec.Body.String())
		}
	})

	t.Run("accepts with x-api-key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
		req.Header.Set("X-API-Key", key)
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("got 401 despite valid X-API-Key; body=%s", rec.Body.String())
		}
	})
}

// TestServer_AuthAllowsCORSOptionsPreflight guards the contract that the
// CORS layer runs OUTSIDE the auth layer: a browser preflight must not be
// rejected with 401, otherwise the front-end can never learn which auth
// header to send. OPTIONS is short-circuited by corsMiddleware before the
// auth check runs.
func TestServer_AuthAllowsCORSOptionsPreflight(t *testing.T) {
	srv := New(WithVersion("test"), WithConfig(&config.Config{APIKey: "k"}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/runtime/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for CORS preflight, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Errorf("expected ACAO header on preflight response")
	}
}

// TestServer_NilConfigDoesNotPanic guards the apiKeyFromConfig helper.
// Construction with no config must not blow up; auth is simply disabled.
func TestServer_NilConfigDoesNotPanic(t *testing.T) {
	srv := New(WithVersion("test")) // no WithConfig → s.cfg == nil

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("nil config should mean no auth, but got 401")
	}
}
