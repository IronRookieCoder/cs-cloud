package membertask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndVerifyManifestDetectsProtectedFileModification(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input", "requirements.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := []MaterialSource{{
		Identity: "requirements", SourceVersion: "v1", RelativePath: "input/requirements.md", Role: MaterialInputProtected,
	}}
	manifest, err := BuildManifest(root, sources)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if manifest.Files[0].SHA256 == "" || manifest.MaterialDigest == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyManifest(root, manifest)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "input_material_modified" {
		t.Fatalf("VerifyManifest error = %#v", err)
	}
}

func TestVerifyManifestAllowsOutputFileModificationAndReportsDirty(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "output", "result.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(root, []MaterialSource{{
		Identity: "result", SourceVersion: "v1", RelativePath: "output/result.md", Role: MaterialOutputWritable,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyManifest(root, manifest)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if !verification.Dirty || len(verification.ChangedPaths) != 1 || verification.ChangedPaths[0] != "output/result.md" {
		t.Fatalf("verification = %+v", verification)
	}
}
