package localserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveUnderRoot(t *testing.T) {
	root := t.TempDir()
	// eval symlinks — TempDir on macOS is itself under /var, a symlink to /private/var
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}
	root = realRoot

	child := filepath.Join(root, "prj-1")
	if err := os.MkdirAll(filepath.Join(child, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name    string
		abs     string
		wantErr error
	}{
		{"direct child", child, nil},
		{"nested child", filepath.Join(child, "sub"), nil},
		{"root itself", root, errWorkspaceIsRoot},
		{"sibling outside root", filepath.Dir(root), errPathEscapes},
		{"traversal escape", filepath.Join(root, "..", "evil"), errPathEscapes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abs, err := filepath.Abs(filepath.Clean(tt.abs))
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			got, err := resolveUnderRoot(abs, root)
			if err != tt.wantErr {
				t.Fatalf("resolveUnderRoot(%q) err = %v, want %v", tt.abs, err, tt.wantErr)
			}
			if err == nil && got == "" {
				t.Fatalf("resolved path is empty")
			}
		})
	}
}

func TestResolveUnderRootSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks on Windows requires admin privileges")
	}
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// symlink inside root -> outside
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := resolveUnderRoot(link, root)
	if err != errPathEscapes {
		t.Fatalf("symlink escape err = %v, want errPathEscapes", err)
	}
}
