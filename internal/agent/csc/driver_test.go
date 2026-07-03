package csc

import (
	"net/http"
	"os/exec"
	"runtime"
	"testing"

	"cs-cloud/internal/agent"
)

func versionEchoCmd(versionStr string) string {
	if runtime.GOOS == "windows" {
		return "cmd /c echo " + versionStr
	}
	return "echo " + versionStr
}

func TestDriverVersion(t *testing.T) {
	d := NewDriver(agent.ParseCommand("csc serve"))
	d.SetVersionCommand(versionEchoCmd("1.2.3 (commit: abc, built: today)"))

	ver, err := d.Version()
	if err != nil {
		t.Fatalf("Version() returned error: %v", err)
	}
	if ver != "1.2.3" {
		t.Errorf("Version() = %q, want %q", ver, "1.2.3")
	}
}

func TestDriverVersionBeta(t *testing.T) {
	d := NewDriver(agent.ParseCommand("csc serve"))
	d.SetVersionCommand(versionEchoCmd("4.2.3-beta (commit: da9b1de20)"))

	ver, err := d.Version()
	if err != nil {
		t.Fatalf("Version() returned error: %v", err)
	}
	if ver != "4.2.3-beta" {
		t.Errorf("Version() = %q, want %q", ver, "4.2.3-beta")
	}
}

func TestDriverVersionCached(t *testing.T) {
	d := NewDriver(agent.ParseCommand("csc serve"))
	d.SetVersionCommand(versionEchoCmd("3.0.0"))

	ver1, _ := d.Version()
	ver2, _ := d.Version()
	if ver1 != ver2 {
		t.Errorf("Version() should be cached: got %q then %q", ver1, ver2)
	}
}

func TestDriverVersionDefaultCli(t *testing.T) {
	// Version uses CLIBinary directly, works even with empty command
	if _, err := exec.LookPath(CLIBinary); err != nil {
		t.Skipf("%q binary not found in PATH: %v", CLIBinary, err)
	}
	d := NewDriver(agent.Command{})

	ver, err := d.Version()
	if err != nil {
		t.Fatalf("Version() returned error: %v", err)
	}
	if ver == "" {
		t.Error("Version() with empty command should still detect csc CLI version")
	}
}

func TestProxyRoutesIncludeConfig(t *testing.T) {
	d := NewDriver(agent.ParseCommand("csc serve"))
	routes := d.ProxyRoutes()

	assertProxyRoute(t, routes, http.MethodGet, "/agents/config", "/config")
	assertProxyRoute(t, routes, http.MethodPatch, "/agents/config", "/config")
}

func TestProxyRoutesIncludeProviderConfig(t *testing.T) {
	d := NewDriver(agent.ParseCommand("csc serve"))
	routes := d.ProxyRoutes()

	assertProxyRoute(t, routes, http.MethodGet, "/agents/models/config", "/provider/config")
	assertProxyRoute(t, routes, http.MethodPatch, "/agents/models/config", "/provider/config")
}

func assertProxyRoute(t *testing.T, routes []agent.ProxyRoute, method, prefix, wantRewrite string) {
	t.Helper()
	for _, route := range routes {
		if route.Method != method || route.Prefix != prefix {
			continue
		}
		if got := route.Rewrite(nil); got != wantRewrite {
			t.Fatalf("%s %s rewrite = %q, want %s", method, prefix, got, wantRewrite)
		}
		if route.Transform != nil {
			t.Fatalf("%s %s should proxy request bodies without transforming them", method, prefix)
		}
		return
	}
	t.Fatalf("missing %s %s proxy route", method, prefix)
}
