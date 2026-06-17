package localserver

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cs-cloud/internal/logger"
)

type pathData struct {
	Home      string `json:"home" example:"/home/user"`
	Directory string `json:"directory" example:"/home/user/project"`
}

// @Summary      Get path information
// @Description  Returns the user home directory and the resolved absolute workspace directory.
// @Tags         Runtime
// @Produce      json
// @Param        directory  query  string  false  "Target directory (relative to workspace root)"  default(.)
// @Success      200  {object}  envelope{data=pathData}
// @Failure      400  {object}  envelope
// @Router       /runtime/path [get]
func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()

	directory := r.URL.Query().Get("directory")
	if directory == "" {
		directory = "."
	}

	absDir, _, err := s.resolvePath(r, directory)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	writeOK(w, pathData{
		Home:      home,
		Directory: absDir,
	})
}

type vcsData struct {
	Branch          string `json:"branch,omitempty" example:"main"`
	RemoteBranch    string `json:"remoteBranch,omitempty" example:"origin/main"`
	Dirty           bool    `json:"dirty" example:"true"`
	StagedCount     int     `json:"stagedCount,omitempty" example:"2"`
	UnstagedCount   int     `json:"unstagedCount,omitempty" example:"3"`
	UntrackedCount  int     `json:"untrackedCount,omitempty" example:"1"`
	AheadCount      int     `json:"aheadCount" example:"5"`
	BehindCount     int     `json:"behindCount" example:"2"`
	LastCommitHash  string  `json:"lastCommitHash,omitempty" example:"a1b2c3d"`
	LastCommitTime  int64   `json:"lastCommitTime,omitempty" example:"1699200000"`
}

// @Summary      Get Git repository status
// @Description  Returns detailed git status including branch, dirty state, file counts, and sync status. Returns empty object if not a git repository.
// @Tags         Runtime
// @Produce      json
// @Param        directory  query  string  false  "Target directory (relative to workspace root)"  default(.)
// @Success      200  {object}  envelope{data=vcsData}
// @Failure      400  {object}  envelope
// @Router       /runtime/vcs [get]
func (s *Server) handleVcs(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		directory = "."
	}

	absDir, _, err := s.resolvePath(r, directory)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	data := gitStatus(absDir)
	writeOK(w, data)
}

// @Summary      Kill all agents and reinitialize
// @Description  Terminates all running agent instances and restarts the default agent.
// @Tags         Runtime
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]bool}
// @Router       /runtime/dispose [post]
func (s *Server) handleInstanceDispose(w http.ResponseWriter, r *http.Request) {
	workspace := getWorkspaceDir(r)
	if workspace != "" {
		if abs, err := filepath.Abs(filepath.Clean(workspace)); err == nil {
			s.invalidateFindFilesCache(abs)
		} else {
			s.invalidateFindFilesCache(filepath.Clean(workspace))
		}
	} else {
		s.invalidateFindFilesCache("")
	}

	s.manager.KillAll()

	ctx := r.Context()
	if err := s.manager.InitDefaultAgent(ctx, s.cfg.DefaultAgent, "", s.cfg.AgentVersionCommand, s.cfg.AgentWorkspace, nil); err != nil {
		logger.Error("failed to restart agent: %v", err)
	}

	writeOK(w, map[string]any{"disposed": true})
}

func gitStatus(dir string) vcsData {
	data := vcsData{}

	// Get current branch
	branch, err := runGitCommand(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return data
	}
	data.Branch = strings.TrimSpace(branch)

	// Get remote branch
	remoteBranch, err := runGitCommand(dir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+data.Branch)
	if err == nil {
		data.RemoteBranch = strings.TrimSpace(remoteBranch)
	}

	// Get status for dirty state and file counts
	status, err := runGitCommand(dir, "status", "--porcelain")
	if err == nil {
		lines := strings.Split(strings.TrimSpace(status), "\n")
		data.Dirty = len(lines) > 0 && lines[0] != ""

		for _, line := range lines {
			if line == "" {
				continue
			}
			if len(line) > 0 {
				statusCode := line[0]
				switch statusCode {
				case 'M', 'A', 'R', 'C', 'D', 'U': // staged changes
					data.StagedCount++
				}
			}
			if len(line) > 1 {
				statusCode := line[1]
				switch statusCode {
				case 'M', 'D': // unstaged changes
					data.UnstagedCount++
				case '?': // untracked
					data.UntrackedCount++
				}
			}
		}
	}

	// Get ahead/behind counts
	if data.RemoteBranch != "" {
		aheadBehind, err := runGitCommand(dir, "rev-list", "--left-right", "--count", data.RemoteBranch+"...HEAD")
		if err == nil {
			parts := strings.Split(strings.TrimSpace(aheadBehind), "\t")
			if len(parts) == 2 {
				fmt.Sscanf(parts[1], "%d", &data.AheadCount)   // local commits
				fmt.Sscanf(parts[0], "%d", &data.BehindCount)  // remote commits
			}
		}
	}

	// Get last commit info
	lastCommit, err := runGitCommand(dir, "log", "-1", "--format=%H|%ct")
	if err == nil {
		parts := strings.Split(strings.TrimSpace(lastCommit), "|")
		if len(parts) >= 2 {
			hash := strings.TrimSpace(parts[0])
			if len(hash) > 7 {
				data.LastCommitHash = hash[:7] // short hash
			}
			fmt.Sscanf(parts[1], "%d", &data.LastCommitTime)
		}
	}

	return data
}

func runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
