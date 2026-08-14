package membertask

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

type RepoStepStatus string

const (
	RepoStepNotStarted RepoStepStatus = "not_started"
	RepoStepRunning    RepoStepStatus = "running"
	RepoStepWritten    RepoStepStatus = "written"
	RepoStepVerified   RepoStepStatus = "verified"
	RepoStepConflict   RepoStepStatus = "conflict"
	RepoStepUnknown    RepoStepStatus = "unknown"
)

type RepoStepResult struct {
	RepositoryIdentity string         `json:"repository_identity"`
	Status             RepoStepStatus `json:"status"`
	ObservedSHA        string         `json:"observed_sha,omitempty"`
}

type gitRunner func(ctx context.Context, dir string, args ...string) (string, error)

type GitPublisher struct {
	run gitRunner
}

func NewGitPublisher() *GitPublisher {
	return newGitPublisherWithRunner(func(ctx context.Context, dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		output, err := cmd.Output()
		if err != nil {
			return "", errors.New("git command failed")
		}
		return strings.TrimSpace(string(output)), nil
	})
}

func newGitPublisherWithRunner(run gitRunner) *GitPublisher {
	return &GitPublisher{run: run}
}

func (p *GitPublisher) PublishStep(ctx context.Context, worktree string, plan RepoPublishPlan) (RepoStepResult, error) {
	return p.PublishStepWithRemote(ctx, worktree, plan, "")
}

func (p *GitPublisher) PublishStepWithRemote(ctx context.Context, worktree string, plan RepoPublishPlan, remoteURL string) (RepoStepResult, error) {
	if !strings.HasPrefix(plan.ExpectedRef, "refs/heads/") || plan.ExpectedRef == "refs/heads/main" || plan.ExpectedRef == "refs/heads/master" {
		return RepoStepResult{}, newTaskError("invalid_publish_plan", "publish target must be a non-base branch ref", nil)
	}
	remote := plan.RemoteName
	if remote == "" {
		remote = "origin"
	}
	observed, err := p.remoteRef(ctx, worktree, remote, remoteURL, plan.ExpectedRef)
	if err != nil {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepUnknown}, newTaskError("operation_result_unknown", "cannot verify remote ref", err)
	}
	if observed == plan.HeadSHA {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepVerified, ObservedSHA: observed}, nil
	}
	leaseSHA := plan.BeforeSHA
	if observed == "" {
		if !strings.HasPrefix(plan.BaseRef, "refs/heads/") {
			return RepoStepResult{}, newTaskError("invalid_publish_plan", "publish base must be a full branch ref", nil)
		}
		baseObserved, baseErr := p.remoteRef(ctx, worktree, remote, remoteURL, plan.BaseRef)
		if baseErr != nil {
			return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepUnknown}, newTaskError("operation_result_unknown", "cannot verify remote base ref", baseErr)
		}
		if baseObserved != plan.BeforeSHA {
			return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepConflict, ObservedSHA: baseObserved}, newTaskError("remote_ref_changed", "remote base ref changed before publish", nil)
		}
		leaseSHA = ""
	} else if observed != plan.BeforeSHA {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepConflict, ObservedSHA: observed}, newTaskError("remote_ref_changed", "remote ref changed before publish", nil)
	}
	args := gitRemoteOverride(remote, remoteURL, "push", remote, plan.HeadSHA+":"+plan.ExpectedRef, "--force-with-lease="+plan.ExpectedRef+":"+leaseSHA)
	if _, err := p.run(ctx, worktree, args...); err != nil {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepUnknown}, newTaskError("operation_result_unknown", "git push result is unknown", err)
	}
	observed, err = p.remoteRef(ctx, worktree, remote, remoteURL, plan.ExpectedRef)
	if err != nil {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepUnknown}, newTaskError("operation_result_unknown", "cannot verify published ref", err)
	}
	if observed != plan.HeadSHA {
		return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepConflict, ObservedSHA: observed}, newTaskError("operation_ref_conflict", "published ref does not match planned head", nil)
	}
	return RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepVerified, ObservedSHA: observed}, nil
}

func (p *GitPublisher) remoteRef(ctx context.Context, worktree, remote, remoteURL, ref string) (string, error) {
	output, err := p.run(ctx, worktree, gitRemoteOverride(remote, remoteURL, "ls-remote", "--refs", remote, ref)...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != ref {
		return "", errors.New("unexpected ls-remote response")
	}
	return fields[0], nil
}

func gitRemoteOverride(remote, remoteURL string, args ...string) []string {
	if remoteURL == "" {
		return args
	}
	return append([]string{"-c", "remote." + remote + ".url=" + remoteURL}, args...)
}
