package membertask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfilePermissionsAcceptSecuredRootAndSecret(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "member_task_secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureProfilePermissions(root, secret); err != nil {
		t.Fatalf("SecureProfilePermissions: %v", err)
	}
	if err := ValidateProfilePermissions(root, secret); err != nil {
		t.Fatalf("ValidateProfilePermissions: %v", err)
	}
}
