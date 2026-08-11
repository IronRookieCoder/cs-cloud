package membertask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutTaskDirUsesTaskIdentity(t *testing.T) {
	root := t.TempDir()
	key, _ := ParseTaskKey("cloud/ws/node/critic")
	got := (Layout{ProfileRoot: root}).TaskDir(key)
	want := filepath.Join(root, "member-tasks", "cloud", "ws", "node-critic")
	if got != want {
		t.Fatalf("TaskDir = %q, want %q", got, want)
	}
}

func TestValidateContainedPathRejectsLexicalEscape(t *testing.T) {
	root := t.TempDir()
	if err := ValidateContainedPath(root, filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("expected lexical escape to be rejected")
	}
}

func TestValidateContainedPathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "member-tasks")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateContainedPath(root, filepath.Join(link, "file.txt")); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
