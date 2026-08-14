//go:build linux

package autostart

import (
	"testing"
)

func TestSystemdEnabled_TrueOnEnabledStdout(t *testing.T) {
	m := &systemdUserManager{
		cfg: Config{Name: "cs-cloud"},
		shell: &fakeRunner{response: map[string]fakeResp{
			"systemctl --user is-enabled cs-cloud.service": {out: []byte("enabled\n")},
		}},
	}
	got, err := m.Enabled()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got {
		t.Errorf("expected enabled=true")
	}
}

func TestSystemdEnabled_FalseOnDisabledStdout(t *testing.T) {
	m := &systemdUserManager{
		cfg: Config{Name: "cs-cloud"},
		shell: &fakeRunner{response: map[string]fakeResp{
			"systemctl --user is-enabled cs-cloud.service": {out: []byte("disabled\n")},
		}},
	}
	got, err := m.Enabled()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Errorf("expected enabled=false")
	}
}

func TestLingerStatus_ParsesYes(t *testing.T) {
	// currentUserName() picks up USER first; pin it so the lookup key is
	// deterministic across sandboxed CI runs.
	t.Setenv("USER", "testuser")
	t.Setenv("LOGNAME", "testuser")
	got, err := LingerStatus(&fakeRunner{response: map[string]fakeResp{
		"loginctl show-user testuser --property=Linger": {out: []byte("Linger=yes\n")},
	}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "yes" {
		t.Errorf("expected Linger=yes, got %q", got)
	}
}

func TestLingerStatus_ParsesNo(t *testing.T) {
	t.Setenv("USER", "testuser")
	got, err := LingerStatus(&fakeRunner{response: map[string]fakeResp{
		"loginctl show-user testuser --property=Linger": {out: []byte("Linger=no\n")},
	}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "no" {
		t.Errorf("expected Linger=no, got %q", got)
	}
}
