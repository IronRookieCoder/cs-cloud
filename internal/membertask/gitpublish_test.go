package membertask

import (
	"context"
	"reflect"
	"testing"
)

func TestForceWithLeasePublishesOnlyExpectedRef(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch len(calls) {
		case 1:
			return "before\trefs/heads/task/result", nil
		case 2:
			return "", nil
		default:
			return "head\trefs/heads/task/result", nil
		}
	}
	publisher := newGitPublisherWithRunner(runner)
	plan := RepoPublishPlan{RepositoryIdentity: "repo", ExpectedRef: "refs/heads/task/result", BeforeSHA: "before", HeadSHA: "head", RemoteName: "origin"}
	result, err := publisher.PublishStep(context.Background(), t.TempDir(), plan)
	if err != nil {
		t.Fatalf("PublishStep: %v", err)
	}
	if result.Status != RepoStepVerified {
		t.Fatalf("result = %+v", result)
	}
	wantPush := []string{"push", "origin", "head:refs/heads/task/result", "--force-with-lease=refs/heads/task/result:before"}
	if !reflect.DeepEqual(calls[1], wantPush) {
		t.Fatalf("push args = %#v, want %#v", calls[1], wantPush)
	}
}

func TestForceWithLeaseCreatesMissingTargetFromUnchangedBase(t *testing.T) {
	var calls [][]string
	publisher := newGitPublisherWithRunner(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch len(calls) {
		case 1:
			return "", nil
		case 2:
			return "before\trefs/heads/main", nil
		case 3:
			return "", nil
		default:
			return "head\trefs/heads/task/result", nil
		}
	})
	plan := RepoPublishPlan{RepositoryIdentity: "repo", ExpectedRef: "refs/heads/task/result", BaseRef: "refs/heads/main", BeforeSHA: "before", HeadSHA: "head", RemoteName: "origin"}
	result, err := publisher.PublishStep(context.Background(), t.TempDir(), plan)
	if err != nil || result.Status != RepoStepVerified {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	wantPush := []string{"push", "origin", "head:refs/heads/task/result", "--force-with-lease=refs/heads/task/result:"}
	if !reflect.DeepEqual(calls[2], wantPush) {
		t.Fatalf("push args = %#v, want %#v", calls[2], wantPush)
	}
}

func TestForceWithLeaseUsesEphemeralAuthenticatedRemote(t *testing.T) {
	var calls [][]string
	publisher := newGitPublisherWithRunner(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return "before\trefs/heads/task/result", nil
		}
		if len(calls) == 3 {
			return "head\trefs/heads/task/result", nil
		}
		return "", nil
	})
	plan := RepoPublishPlan{RepositoryIdentity: "repo", ExpectedRef: "refs/heads/task/result", BeforeSHA: "before", HeadSHA: "head", RemoteName: "origin"}
	remoteURL := "https://oauth2:repo-token@gitea.example/team/repo.git"
	if _, err := publisher.PublishStepWithRemote(context.Background(), t.TempDir(), plan, remoteURL); err != nil {
		t.Fatalf("PublishStepWithRemote: %v", err)
	}
	prefix := []string{"-c", "remote.origin.url=" + remoteURL}
	for i, call := range calls {
		if len(call) < 2 || !reflect.DeepEqual(call[:2], prefix) {
			t.Fatalf("call %d = %#v, want temporary remote override", i, call)
		}
	}
}

func TestForceWithLeaseDetectsRemoteChangeBeforeWrite(t *testing.T) {
	publisher := newGitPublisherWithRunner(func(context.Context, string, ...string) (string, error) {
		return "other\trefs/heads/task/result", nil
	})
	_, err := publisher.PublishStep(context.Background(), t.TempDir(), RepoPublishPlan{
		ExpectedRef: "refs/heads/task/result", BeforeSHA: "before", HeadSHA: "head", RemoteName: "origin",
	})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "remote_ref_changed" {
		t.Fatalf("error = %#v", err)
	}
}

func TestForceWithLeaseRecognizesAlreadyWrittenHead(t *testing.T) {
	var calls int
	publisher := newGitPublisherWithRunner(func(context.Context, string, ...string) (string, error) {
		calls++
		return "head\trefs/heads/task/result", nil
	})
	result, err := publisher.PublishStep(context.Background(), t.TempDir(), RepoPublishPlan{
		ExpectedRef: "refs/heads/task/result", BeforeSHA: "before", HeadSHA: "head", RemoteName: "origin",
	})
	if err != nil || result.Status != RepoStepVerified || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}
