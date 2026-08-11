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
