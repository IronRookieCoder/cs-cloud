package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRunRepoCheckout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repo/checkout" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) == 0 {
			t.Error("empty body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"path":"/work/repo"}}`))
	}))
	defer srv.Close()

	t.Setenv("CS_CLOUD_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TASK_ID", "t1")

	cfg := checkoutConfig{repoURL: "https://gitlab/o/r.git"}
	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runRepoCheckout(cfg)
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatalf("runRepoCheckout: %v", err)
	}
	if strings.TrimSpace(string(out)) != "/work/repo" {
		t.Errorf("stdout = %q, want /work/repo", strings.TrimSpace(string(out)))
	}
}

func TestRunRepoCheckout_MissingEnv(t *testing.T) {
	t.Setenv("CS_CLOUD_SERVER_URL", "")
	t.Setenv("MULTICA_TASK_ID", "")
	cfg := checkoutConfig{repoURL: "https://gitlab/o/r.git"}
	if err := runRepoCheckout(cfg); err == nil {
		t.Error("expected error when CS_CLOUD_SERVER_URL missing")
	}
}

func TestParseCheckoutArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want checkoutConfig
	}{
		{"url only", []string{"https://gitlab/o/r.git"}, checkoutConfig{repoURL: "https://gitlab/o/r.git"}},
		{"base + url", []string{"--base", "develop", "https://gitlab/o/r.git"}, checkoutConfig{repoURL: "https://gitlab/o/r.git", baseBranch: "develop"}},
		{"short base + url", []string{"-b", "main", "https://gitlab/o/r.git"}, checkoutConfig{repoURL: "https://gitlab/o/r.git", baseBranch: "main"}},
	}
	for _, c := range cases {
		got, err := parseCheckoutArgs(c.args)
		if err != nil {
			t.Fatalf("%s: parse(%v): %v", c.name, c.args, err)
		}
		if got != c.want {
			t.Errorf("%s: parse(%v) = %+v, want %+v", c.name, c.args, got, c.want)
		}
	}
}

func TestParseCheckoutArgs_Errors(t *testing.T) {
	if _, err := parseCheckoutArgs(nil); err == nil {
		t.Error("expected error for missing repo url")
	}
	if _, err := parseCheckoutArgs([]string{"--base", "develop"}); err == nil {
		t.Error("expected error for --base without url")
	}
	if _, err := parseCheckoutArgs([]string{"--bogus", "x"}); err == nil {
		t.Error("expected error for unknown flag")
	}
}
