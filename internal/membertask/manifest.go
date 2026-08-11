package membertask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileManifest struct {
	Identity      string       `json:"identity"`
	SourceVersion string       `json:"source_version"`
	RelativePath  string       `json:"relative_path"`
	Role          MaterialRole `json:"role"`
	SHA256        string       `json:"sha256"`
}

type RepositoryManifest struct {
	Identity       string            `json:"identity"`
	RelativePath   string            `json:"relative_path"`
	Role           MaterialRole      `json:"role"`
	BaseSHA        string            `json:"base_sha"`
	BaseRef        string            `json:"base_ref"`
	BeforeSHA      string            `json:"before_sha"`
	BaselineHead   string            `json:"baseline_head"`
	TargetRef      string            `json:"target_ref"`
	ProtectedBlobs map[string]string `json:"protected_blobs,omitempty"`
	OutputPaths    []string          `json:"output_paths,omitempty"`
}

type Manifest struct {
	SchemaVersion  string               `json:"schema_version"`
	Files          []FileManifest       `json:"files,omitempty"`
	Repositories   []RepositoryManifest `json:"repositories,omitempty"`
	MaterialDigest string               `json:"material_digest"`
}

type Verification struct {
	Dirty         bool     `json:"dirty"`
	ChangedPaths  []string `json:"changed_paths,omitempty"`
	ContentDigest string   `json:"content_digest"`
}

func BuildManifest(root string, sources []MaterialSource) (Manifest, error) {
	manifest := Manifest{SchemaVersion: SchemaVersion}
	for _, source := range sources {
		rel, err := cleanRelativePath(source.RelativePath)
		if err != nil {
			return Manifest{}, err
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := ValidateContainedPath(root, full); err != nil {
			return Manifest{}, err
		}
		switch source.Kind {
		case "", "file":
			digest, err := hashFile(full)
			if err != nil {
				return Manifest{}, newTaskError("material_missing", "prepared file material is missing", err)
			}
			if source.SHA256 != "" && !strings.EqualFold(source.SHA256, digest) {
				return Manifest{}, newTaskError("input_material_modified", "material hash does not match cloud context", nil)
			}
			manifest.Files = append(manifest.Files, FileManifest{Identity: source.Identity, SourceVersion: source.SourceVersion, RelativePath: rel, Role: source.Role, SHA256: digest})
		case "git":
			if source.Repository == nil {
				return Manifest{}, newTaskError("invalid_cloud_response", "git material has no repository context", nil)
			}
			repo, err := buildRepositoryManifest(full, rel, source)
			if err != nil {
				return Manifest{}, err
			}
			manifest.Repositories = append(manifest.Repositories, repo)
		default:
			return Manifest{}, newTaskError("provider_not_supported", "material kind is not supported", nil)
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].RelativePath < manifest.Files[j].RelativePath })
	sort.Slice(manifest.Repositories, func(i, j int) bool { return manifest.Repositories[i].Identity < manifest.Repositories[j].Identity })
	for i := range manifest.Repositories {
		sort.Strings(manifest.Repositories[i].OutputPaths)
	}
	digestInput := struct {
		Files        []FileManifest       `json:"files"`
		Repositories []RepositoryManifest `json:"repositories"`
	}{manifest.Files, manifest.Repositories}
	manifest.MaterialDigest = digestJSON(digestInput)
	return manifest, nil
}

func VerifyManifest(root string, manifest Manifest) (Verification, error) {
	changed := make(map[string]struct{})
	content := make(map[string]string)
	for _, file := range manifest.Files {
		full := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if err := ValidateContainedPath(root, full); err != nil {
			return Verification{}, err
		}
		digest, err := hashFile(full)
		if err != nil {
			return Verification{}, newTaskError("input_material_modified", "prepared material is missing", err)
		}
		if digest != file.SHA256 {
			if file.Role != MaterialOutputWritable {
				return Verification{}, newTaskError("input_material_modified", "protected material changed", nil)
			}
			changed[file.RelativePath] = struct{}{}
			content[file.RelativePath] = digest
		}
	}
	for _, repo := range manifest.Repositories {
		repoDir := filepath.Join(root, filepath.FromSlash(repo.RelativePath))
		repoChanged, head, err := verifyRepository(repoDir, repo)
		if err != nil {
			return Verification{}, err
		}
		for _, repoPath := range repoChanged {
			changed[repo.RelativePath+"/"+repoPath] = struct{}{}
		}
		content[repo.Identity] = head
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return Verification{Dirty: len(paths) > 0, ChangedPaths: paths, ContentDigest: digestJSON(content)}, nil
}

func buildRepositoryManifest(repoDir, rel string, source MaterialSource) (RepositoryManifest, error) {
	repo := source.Repository
	head, err := gitOutput(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return RepositoryManifest{}, newTaskError("git_material_invalid", "cannot resolve repository HEAD", err)
	}
	if repo.BaseSHA != "" && head != repo.BaseSHA {
		return RepositoryManifest{}, newTaskError("input_material_modified", "repository HEAD does not match exact base SHA", nil)
	}
	protected := make(map[string]string, len(repo.ProtectedPaths))
	for _, raw := range repo.ProtectedPaths {
		path, err := cleanRelativePath(raw)
		if err != nil {
			return RepositoryManifest{}, err
		}
		oid, err := gitOutput(repoDir, "rev-parse", "HEAD:"+path)
		if err != nil {
			return RepositoryManifest{}, newTaskError("input_material_modified", "protected repository material is missing", err)
		}
		protected[path] = oid
	}
	outputs := make([]string, 0, len(repo.OutputPaths))
	for _, raw := range repo.OutputPaths {
		path, err := cleanRelativePath(raw)
		if err != nil {
			return RepositoryManifest{}, err
		}
		outputs = append(outputs, path)
	}
	beforeSHA := repo.BeforeSHA
	if beforeSHA == "" {
		beforeSHA = repo.BaseSHA
	}
	return RepositoryManifest{Identity: repo.Identity, RelativePath: rel, Role: source.Role, BaseSHA: head, BaseRef: repo.BaseRef, BeforeSHA: beforeSHA, BaselineHead: head, TargetRef: repo.TargetRef, ProtectedBlobs: protected, OutputPaths: outputs}, nil
}

func verifyRepository(repoDir string, manifest RepositoryManifest) ([]string, string, error) {
	head, err := gitOutput(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", newTaskError("git_material_invalid", "cannot resolve repository HEAD", err)
	}
	if _, err := gitOutput(repoDir, "merge-base", "--is-ancestor", manifest.BaseSHA, head); err != nil {
		return nil, "", newTaskError("input_material_modified", "repository no longer contains the prepared base", nil)
	}
	for path, expected := range manifest.ProtectedBlobs {
		oid, err := gitOutput(repoDir, "rev-parse", "HEAD:"+path)
		if err != nil || oid != expected {
			return nil, "", newTaskError("input_material_modified", "protected repository material changed", nil)
		}
	}
	changed := map[string]struct{}{}
	committed, err := gitOutputAllowEmpty(repoDir, "diff", "--name-only", manifest.BaseSHA+".."+head)
	if err != nil {
		return nil, "", newTaskError("git_material_invalid", "cannot inspect repository commits", err)
	}
	for _, path := range splitLines(committed) {
		changed[path] = struct{}{}
	}
	status, err := gitOutputAllowEmpty(repoDir, "status", "--porcelain")
	if err != nil {
		return nil, "", newTaskError("git_material_invalid", "cannot inspect repository worktree", err)
	}
	for _, line := range splitLines(status) {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if _, after, ok := strings.Cut(path, " -> "); ok {
			path = after
		}
		changed[filepath.ToSlash(path)] = struct{}{}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		if manifest.Role != MaterialOutputWritable {
			return nil, "", newTaskError("input_material_modified", "reference repository changed", nil)
		}
		if !pathAllowed(path, manifest.OutputPaths) {
			return nil, "", newTaskError("input_material_modified", "repository changed outside writable outputs", nil)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, head, nil
}

func pathAllowed(path string, roots []string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

func cleanRelativePath(raw string) (string, error) {
	if raw == "" || strings.Contains(raw, "\\") || strings.ContainsRune(raw, '\x00') {
		return "", newTaskError("unsafe_task_path", "material path is invalid", nil)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(raw)) {
		return "", newTaskError("unsafe_task_path", "material path escapes task root", nil)
	}
	return clean, nil
}

func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func digestJSON(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "", errors.New("git command failed")
	}
	return strings.TrimSpace(string(b)), nil
}

func gitOutputAllowEmpty(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "", errors.New("git command failed")
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func splitLines(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(strings.Trim(raw, "\r\n"), "\r\n", "\n"), "\n")
}
