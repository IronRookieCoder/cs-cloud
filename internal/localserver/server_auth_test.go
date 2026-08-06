package localserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/config"
)

func TestEnsureLocalAPIKey_GeneratesWhenUnset(t *testing.T) {
	cfg := &config.Config{}
	ensureLocalAPIKey(cfg)
	if cfg.APIKey == "" {
		t.Fatal("expected an ephemeral api key to be generated when unset")
	}
	if len(cfg.APIKey) < 32 {
		t.Fatalf("generated key too short (%d chars), want at least 32 for a strong key", len(cfg.APIKey))
	}
}

func TestEnsureLocalAPIKey_PreservesConfiguredKey(t *testing.T) {
	cfg := &config.Config{APIKey: "preset-key"}
	ensureLocalAPIKey(cfg)
	if cfg.APIKey != "preset-key" {
		t.Fatalf("cfg.APIKey = %q, want preset-key preserved", cfg.APIKey)
	}
}

func TestEnsureLocalAPIKey_NilSafe(t *testing.T) {
	ensureLocalAPIKey(nil) // must not panic
}

// TestServer_AutoGeneratesKeyWhenUnconfigured verifies that a server built
// without an API key auto-generates one, so the localserver is never left as
// an unauthenticated passthrough by default.
func TestServer_AutoGeneratesKeyWhenUnconfigured(t *testing.T) {
	srv := New(WithVersion("test"), WithConfig(&config.Config{}))
	if srv.cfg.APIKey == "" {
		t.Fatal("expected New to auto-generate an api key when none is configured")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (auto-generated key must enforce auth)", rec.Code)
	}
}
