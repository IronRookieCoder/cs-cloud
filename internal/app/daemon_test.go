package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cs-cloud/internal/membertask"
	"cs-cloud/internal/platform"
)

func TestPrepareCloudDaemonRejectsMissingDeviceAndClearsReadiness(t *testing.T) {
	dataDir := t.TempDir()
	previousDataDir := platform.DataDir()
	platform.SetDataDir(dataDir)
	t.Cleanup(func() { platform.SetDataDir(previousDataDir) })

	app := &App{rootDir: filepath.Join(dataDir, "cs-cloud")}
	if err := app.WritePID(12345); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveState("running"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveServerURL("http://127.0.0.1:9876"); err != nil {
		t.Fatal(err)
	}

	_, err := app.PrepareCloudDaemon()
	var taskErr *membertask.TaskError
	if !errors.As(err, &taskErr) || taskErr.Code != "device_registration_required" {
		t.Fatalf("error = %#v, want device_registration_required", err)
	}
	for _, path := range []string{app.pidFile(), app.stateFile(), app.serverFile()} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("readiness artifact %q still exists: %v", path, statErr)
		}
	}
}

func TestDataDirFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, ""},
		{"no data-dir flag", []string{"serve", "--mode", "cloud"}, ""},
		{"data-dir with space separator", []string{"serve", "--data-dir", "/tmp/mydata"}, "/tmp/mydata"},
		{"data-dir with equals sign", []string{"serve", "--data-dir=/tmp/mydata"}, "/tmp/mydata"},
		{"data-dir at end without value", []string{"serve", "--data-dir"}, ""},
		{"data-dir with quoted path", []string{"--data-dir", `"C:\My Data"`}, `"C:\My Data"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dataDirFromArgs(tt.args)
			if got != tt.want {
				t.Errorf("dataDirFromArgs(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestRootDirFromDataDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".costrict", "cs-cloud")

	t.Run("empty uses default", func(t *testing.T) {
		got := rootDirFromDataDir("")
		if got != defaultRoot {
			t.Errorf("rootDirFromDataDir(\"\") = %q, want %q", got, defaultRoot)
		}
	})

	t.Run("custom data dir", func(t *testing.T) {
		dataDir := filepath.Join(os.TempDir(), "mydata")
		got := rootDirFromDataDir(dataDir)
		want := filepath.Join(dataDir, "cs-cloud")
		if got != want {
			t.Errorf("rootDirFromDataDir(%q) = %q, want %q", dataDir, got, want)
		}
	})

	t.Run("relative path resolves to absolute", func(t *testing.T) {
		got := rootDirFromDataDir(".")
		if !filepath.IsAbs(got) {
			t.Errorf("rootDirFromDataDir(\".\") = %q, expected absolute path", got)
		}
	})
}

func TestHealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	app := &App{rootDir: t.TempDir()}
	if err := app.SaveServerURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	if !app.healthCheck() {
		t.Error("healthCheck() = false, want true")
	}
}

func TestHealthCheck_Failure(t *testing.T) {
	app := &App{rootDir: t.TempDir()}
	if app.healthCheck() {
		t.Error("healthCheck() = true with no server URL, want false")
	}
}

func TestHealthCheck_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	app := &App{rootDir: t.TempDir()}
	if err := app.SaveServerURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	if app.healthCheck() {
		t.Error("healthCheck() = true with 503, want false")
	}
}

func TestDaemonStatus_PIDFileNotFound(t *testing.T) {
	app := &App{rootDir: t.TempDir()}
	running, pid, reason := app.DaemonStatus()
	if running {
		t.Error("DaemonStatus() running = true, want false")
	}
	if pid != 0 {
		t.Errorf("DaemonStatus() pid = %d, want 0", pid)
	}
	if reason != "pid file not found" {
		t.Errorf("DaemonStatus() reason = %q, want %q", reason, "pid file not found")
	}
}

func TestDaemonStatus_HealthCheckFails(t *testing.T) {
	dir := t.TempDir()
	app := &App{rootDir: dir}
	app.WritePID(os.Getpid())
	app.SaveState("running")

	running, _, reason := app.DaemonStatus()
	if running {
		t.Error("DaemonStatus() running = true with no server, want false")
	}
	if reason != "health check failed" {
		t.Errorf("DaemonStatus() reason = %q, want %q", reason, "health check failed")
	}
}

func TestDaemonStatus_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	app := &App{rootDir: dir}
	app.WritePID(os.Getpid())
	app.SaveState("running")
	app.SaveServerURL(srv.URL)

	running, pid, reason := app.DaemonStatus()
	if !running {
		t.Errorf("DaemonStatus() running = false, want true (reason=%q)", reason)
	}
	if pid != os.Getpid() {
		t.Errorf("DaemonStatus() pid = %d, want %d", pid, os.Getpid())
	}
	if reason != "" {
		t.Errorf("DaemonStatus() reason = %q, want empty", reason)
	}
}

func TestMemberTaskDaemonReadyRequiresLiveProcessAndHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runtime/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	app := &App{rootDir: t.TempDir()}
	if err := app.SaveServerURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	if app.MemberTaskDaemonReady() {
		t.Fatal("ready without a daemon pid")
	}
	if err := app.WritePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if !app.MemberTaskDaemonReady() {
		t.Fatal("not ready with a live pid and healthy private server")
	}
}
