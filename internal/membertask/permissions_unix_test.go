//go:build !windows

package membertask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfilePermissionsRejectWorldReadableSecret(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "member_task_secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureProfilePermissions(root, secret); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfilePermissions(root, secret); err == nil {
		t.Fatal("world-readable secret was accepted")
	}
}
