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

func TestMaterialOriginOmitsRelativePathOnlyMetadata(t *testing.T) {
	if origin := materialOrigin(MaterialSource{}, "input/file.txt"); origin != nil {
		t.Fatalf("materialOrigin = %+v, want nil", origin)
	}
}

func TestBuildManifestPersistsDeliverableSnapshotOrigin(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input", "predecessors", "result.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(root, []MaterialSource{{
		Identity: "result", Kind: "file", SourceVersion: "commit-123", RelativePath: "input/predecessors/result.md", Role: MaterialReferenceOnly,
		SHA256: "a4c3ed04a95a3da14a9d235c83d868bed7c0f45cf7f3faa751ee8f50598d2211",
		Source: &DeliverableSource{Provider: "gitea", Repository: "team/repo", Ref: "refs/heads/task/result", Commit: "commit-123", Path: "docs/result.md"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	origin := manifest.Files[0].Origin
	if origin == nil || origin.Provider != "gitea" || origin.RepositoryIdentity != "team/repo" || origin.CommitSHA != "commit-123" || origin.SourcePath != "docs/result.md" || origin.SourceRef != "refs/heads/task/result" {
		t.Fatalf("origin = %#v", origin)
	}
	if manifest.SchemaVersion != SnapshotSchemaVersion {
		t.Fatalf("schema version = %q, want %q", manifest.SchemaVersion, SnapshotSchemaVersion)
	}
}

func TestBuildManifestKeepsLegacySchemaWithoutSnapshotFields(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "legacy.md"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(root, []MaterialSource{{Identity: "legacy", RelativePath: "legacy.md", Role: MaterialReferenceOnly}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != StoreSchemaVersion {
		t.Fatalf("schema version = %q, want %q", manifest.SchemaVersion, StoreSchemaVersion)
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

func TestVerifyManifestRejectsRepositoryOutsideTaskRoot(t *testing.T) {
	root := t.TempDir()
	_, err := VerifyManifest(root, Manifest{Repositories: []RepositoryManifest{{
		Identity: "outside", RelativePath: "../outside", BaseSHA: "deadbeef",
	}}})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "unsafe_task_path" {
		t.Fatalf("VerifyManifest error = %#v, want unsafe_task_path", err)
	}
}

func TestVerifyManifestRejectsReferenceRepositoryCommitInOutputPath(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "init")
	runGitTest(t, repoDir, "config", "user.email", "test@example.com")
	runGitTest(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "result.txt"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "result.txt")
	runGitTest(t, repoDir, "commit", "-m", "base")
	base := runGitTest(t, repoDir, "rev-parse", "HEAD")
	manifest, err := BuildManifest(root, []MaterialSource{{
		Identity: "repo", Kind: "git", RelativePath: "repo", Role: MaterialReferenceOnly,
		Repository: &RepositoryContext{Identity: "repo", BaseSHA: base, OutputPaths: []string{"result.txt"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "result.txt"), []byte("critic change"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "result.txt")
	runGitTest(t, repoDir, "commit", "-m", "critic changed output")

	_, err = VerifyManifest(root, manifest)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "input_material_modified" {
		t.Fatalf("VerifyManifest error = %#v, want input_material_modified", err)
	}
}
