package localserver

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type diffFileEntry struct {
	Path      string `json:"path" example:"src/main.go"`
	Status    string `json:"status" example:"modified"`
	Additions int    `json:"additions" example:"10"`
	Deletions int    `json:"deletions" example:"3"`
}

type diffData struct {
	Directory      string          `json:"directory" example:"/home/user/project"`
	Branch         string          `json:"branch" example:"main"`
	StagedFiles    []diffFileEntry `json:"stagedFiles"`
	UnstagedFiles  []diffFileEntry `json:"unstagedFiles"`
	UntrackedFiles []diffFileEntry `json:"untrackedFiles"`
}

type diffContentData struct {
	Diff   string `json:"diff"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// @Summary      Get Git diff statistics
// @Description  Returns staged and unstaged file change statistics. Does not include diff content — use /runtime/diff/content for that.
// @Tags         Runtime
// @Produce      json
// @Param        directory  query  string  false  "Target directory (relative to workspace root)"  default(.)
// @Param        path       query  string  false  "Filter by file path"
// @Success      200  {object}  envelope{data=diffData}
// @Failure      400  {object}  envelope
// @Router       /runtime/diff [get]
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		directory = "."
	}

	absDir, _, err := s.resolvePath(r, directory)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	filterPath := r.URL.Query().Get("path")

	var (
		branch         string
		stagedFiles    []diffFileEntry = []diffFileEntry{}
		unstagedFiles  []diffFileEntry = []diffFileEntry{}
		untrackedFiles []diffFileEntry = []diffFileEntry{}
		mu             sync.Mutex
		wg             sync.WaitGroup
		notGit         bool
	)

	wg.Add(4)

	go func() {
		defer wg.Done()
		b, err := exec.Command("git", "-c", "core.quotePath=false", "-C", absDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			// Only mark as not a git repo if the basic rev-parse command fails
			if isNotGitRepoError(absDir) {
				mu.Lock()
				notGit = true
				mu.Unlock()
			}
			return
		}
		branch = strings.TrimSpace(string(b))
	}()

	go func() {
		defer wg.Done()
		entries, err := parseDiffStatErr(absDir, true, filterPath)
		if err != nil {
			// Don't mark as notGit - diff commands can fail for other reasons
			return
		}
		stagedFiles = entries
	}()

	go func() {
		defer wg.Done()
		entries, err := parseDiffStatErr(absDir, false, filterPath)
		if err != nil {
			// Don't mark as notGit - diff commands can fail for other reasons
			return
		}
		unstagedFiles = entries
	}()

	go func() {
		defer wg.Done()
		entries, err := parseUntrackedFiles(absDir, filterPath)
		if err != nil {
			// Don't mark as notGit - ls-files can fail for other reasons
			return
		}
		untrackedFiles = entries
	}()

	wg.Wait()

	if notGit {
		writeErr(w, http.StatusBadRequest, "NOT_GIT_REPO", fmt.Sprintf("not a git repository: %s", absDir))
		return
	}

	writeOK(w, diffData{
		Directory:      absDir,
		Branch:         branch,
		StagedFiles:    stagedFiles,
		UnstagedFiles:  unstagedFiles,
		UntrackedFiles: untrackedFiles,
	})
}

func parseDiffStatErr(dir string, staged bool, filterPath string) ([]diffFileEntry, error) {
	args := []string{"-c", "core.quotePath=false", "-C", dir, "diff", "--numstat"}
	if staged {
		args = append(args, "--cached")
	}
	if filterPath != "" {
		args = append(args, "--", filterPath)
	}

	// Try numstat first for detailed stats
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		// Fallback to name-status if numstat fails
		return parseDiffByNameStatus(dir, staged, filterPath)
	}

	var entries []diffFileEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		additions := parseNumstat(parts[0])
		deletions := parseNumstat(parts[1])
		path := parts[2]
		status := "modified"
		if additions < 0 {
			status = "deleted"
		} else if strings.HasPrefix(path, "a/") && strings.Contains(line, "=>") {
			status = "renamed"
		}

		entries = append(entries, diffFileEntry{
			Path:      path,
			Status:    status,
			Additions: additions,
			Deletions: deletions,
		})
	}

	if len(entries) == 0 && filterPath == "" {
		entries = []diffFileEntry{}
	}
	return entries, nil
}

// parseDiffByNameStatus falls back to getting file names without detailed stats
func parseDiffByNameStatus(dir string, staged bool, filterPath string) ([]diffFileEntry, error) {
	args := []string{"-c", "core.quotePath=false", "-C", dir, "diff", "--name-status"}
	if staged {
		args = append(args, "--cached")
	}
	if filterPath != "" {
		args = append(args, "--", filterPath)
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, err
	}

	var entries []diffFileEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		path := strings.Join(parts[1:], " ") // Handle paths with spaces

		// Map git status codes to our status strings
		switch status {
		case "M":
			status = "modified"
		case "D":
			status = "deleted"
		case "A":
			status = "added"
		case "R":
			status = "renamed"
		case "C":
			status = "copied"
		default:
			status = "modified"
		}

		entries = append(entries, diffFileEntry{
			Path:      path,
			Status:    status,
			Additions: 0,
			Deletions: 0,
		})
	}

	if len(entries) == 0 && filterPath == "" {
		entries = []diffFileEntry{}
	}
	return entries, nil
}

func parseUntrackedFiles(dir string, filterPath string) ([]diffFileEntry, error) {
	args := []string{"-c", "core.quotePath=false", "-C", dir, "ls-files", "--others", "--exclude-standard"}
	if filterPath != "" {
		args = append(args, "--", filterPath)
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, err
	}

	var entries []diffFileEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		entries = append(entries, diffFileEntry{
			Path:   line,
			Status: "untracked",
		})
	}

	if len(entries) == 0 && filterPath == "" {
		entries = []diffFileEntry{}
	}
	return entries, nil
}

func runGitDiff(dir string, staged bool, filterPath string) string {
	args := []string{"-c", "core.quotePath=false", "-C", dir, "diff"}
	if staged {
		args = append(args, "--cached")
	}
	if filterPath != "" {
		args = append(args, "--", filterPath)
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// @Summary      Get Git diff content
// @Description  Returns the raw git diff output with optional before/after file content. Directory is determined by X-Workspace-Directory header.
// @Tags         Runtime
// @Produce      json
// @Param        staged  query  string  false  "Show staged diff"  Enums(true, false)  default(false)
// @Param        path    query  string  false  "Filter by file path"
// @Success      200  {object}  envelope{data=diffContentData}
// @Failure      400  {object}  envelope
// @Router       /runtime/diff/content [get]
func (s *Server) handleDiffContent(w http.ResponseWriter, r *http.Request) {
	absDir, _, err := s.resolvePath(r, ".")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	staged := r.URL.Query().Get("staged") == "true"
	filterPath := r.URL.Query().Get("path")

	beforeSource := "index"
	afterSource := "worktree"
	if staged {
		beforeSource = "HEAD"
		afterSource = "index"
	}

	writeOK(w, diffContentData{
		Diff:   runGitDiff(absDir, staged, filterPath),
		Before: gitShowFile(absDir, beforeSource, filterPath),
		After:  gitShowFile(absDir, afterSource, filterPath),
	})
}

func gitShowFile(dir string, source string, path string) string {
	if path == "" {
		return ""
	}

	if source == "worktree" {
		// path is an untrusted client query param; sandbox it against dir
		// (which the caller already validated via resolvePath) on both the
		// lexical and symlink-resolved forms, and reject high-risk secret
		// locations. Mirrors resolvePath's defense so symlink-escape and
		// "../" traversal can't reach arbitrary files outside dir.
		absPath := filepath.Join(dir, path)
		if !pathInDir(absPath, dir) {
			return ""
		}
		// On Windows EvalSymlinks rewrites 8.3 short names to long names, so
		// the resolved absPath can diverge from the lexical dir even for a
		// totally benign workspace. Resolve dir the same way before comparing.
		resolvedDir := dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			resolvedDir = real
		}
		resolved := absPath
		if real, err := filepath.EvalSymlinks(absPath); err == nil {
			resolved = real
		}
		if !pathInDir(resolved, resolvedDir) {
			return ""
		}
		if pathIsHighRisk(absPath) || pathIsHighRisk(resolved) {
			return ""
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return ""
		}
		return string(data)
	}

	var args []string
	switch source {
	case "HEAD":
		args = []string{"-c", "core.quotePath=false", "-C", dir, "show", "HEAD:" + path}
	case "index":
		args = []string{"-c", "core.quotePath=false", "-C", dir, "show", ":" + path}
	default:
		return ""
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// @Summary      Get conversation diff (deprecated)
// @Description  Deprecated. Use GET /runtime/diff instead.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Failure      501  {object}  envelope
// @Deprecated   true
// @Router       /conversations/{id}/diff [get]
func (s *Server) handleConversationDiffDeprecated(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotImplemented, "DEPRECATED", "conversation diff is deprecated, use GET /runtime/diff instead")
}

func parseNumstat(s string) int {
	if s == "-" {
		return 0
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// isNotGitRepoError checks if the error is because the directory is not a git repository
func isNotGitRepoError(dir string) bool {
	// Run a simple git command to check if it's a git repo
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	err := cmd.Run()
	if err != nil {
		// Check if the error message indicates it's not a git repository
		if ee, ok := err.(*exec.ExitError); ok {
			errorMsg := string(ee.Stderr)
			return strings.Contains(errorMsg, "not a git repository") ||
				strings.Contains(errorMsg, "fatal: not a git repository")
		}
		return true
	}
	return false
}
