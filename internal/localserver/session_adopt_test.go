package localserver

import (
	"context"
	"errors"
	"testing"

	"cs-cloud/internal/agent/csc"
	"cs-cloud/internal/workflowrunner"
)

type fakeBindingClient struct {
	binding      *workflowrunner.SessionBinding
	bindingErr   error
	resumeErr    error
	bindingCalls int
	resumeCalls  int
}

func (f *fakeBindingClient) GetSessionBinding(ctx context.Context, sessionID string) (*workflowrunner.SessionBinding, error) {
	f.bindingCalls++
	return f.binding, f.bindingErr
}

func (f *fakeBindingClient) ResumeBeginTask(ctx context.Context, taskID, sessionID string) error {
	f.resumeCalls++
	return f.resumeErr
}

type fakeTurnAdopter struct {
	calls []adoptCall
	err   error
}

type adoptCall struct {
	taskID, sessionID string
	agent             *csc.Agent
}

func (f *fakeTurnAdopter) AdoptUserTurn(ctx context.Context, taskID, sessionID string, agent *csc.Agent) error {
	f.calls = append(f.calls, adoptCall{taskID: taskID, sessionID: sessionID, agent: agent})
	return f.err
}

func TestMaybeAdoptTriggersOnResumableBinding(t *testing.T) {
	agent := csc.NewAgentWithEndpoint("http://test")
	bindings := &fakeBindingClient{
		binding: &workflowrunner.SessionBinding{
			TaskID:    "task-123",
			Resumable: true,
		},
	}
	driver := &fakeTurnAdopter{}
	resolve := func(sessionID string) (*csc.Agent, error) { return agent, nil }

	adopter := &sessionAdopter{
		bindings: bindings,
		driver:   driver,
		resolve:  resolve,
	}

	adopter.maybeAdopt(context.Background(), "session-456")

	if bindings.resumeCalls != 1 {
		t.Fatalf("ResumeBeginTask calls = %d, want 1", bindings.resumeCalls)
	}
	if len(driver.calls) != 1 {
		t.Fatalf("AdoptUserTurn calls = %d, want 1", len(driver.calls))
	}
	call := driver.calls[0]
	if call.taskID != "task-123" {
		t.Errorf("AdoptUserTurn taskID = %q, want task-123", call.taskID)
	}
	if call.sessionID != "session-456" {
		t.Errorf("AdoptUserTurn sessionID = %q, want session-456", call.sessionID)
	}
	if call.agent != agent {
		t.Error("AdoptUserTurn agent mismatch")
	}
}

func TestMaybeAdoptFallsThrough(t *testing.T) {
	agent := csc.NewAgentWithEndpoint("http://test")
	resolve := func(sessionID string) (*csc.Agent, error) { return agent, nil }

	cases := []struct {
		name       string
		binding    *workflowrunner.SessionBinding
		bindingErr error
		resumeErr  error
	}{
		{
			name:       "binding not found",
			bindingErr: workflowrunner.ErrSessionNotBound,
		},
		{
			name:      "task not resumable",
			binding:   &workflowrunner.SessionBinding{TaskID: "task-123", Resumable: true},
			resumeErr: workflowrunner.ErrTaskNotResumable,
		},
		{
			name:       "binding network error",
			bindingErr: errors.New("connection refused"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bindings := &fakeBindingClient{
				binding:    tc.binding,
				bindingErr: tc.bindingErr,
				resumeErr:  tc.resumeErr,
			}
			driver := &fakeTurnAdopter{}
			adopter := &sessionAdopter{
				bindings: bindings,
				driver:   driver,
				resolve:  resolve,
			}

			adopter.maybeAdopt(context.Background(), "session-456")

			if len(driver.calls) != 0 {
				t.Fatalf("AdoptUserTurn calls = %d, want 0", len(driver.calls))
			}
		})
	}
}

func TestMaybeAdoptCachesNegative(t *testing.T) {
	bindings := &fakeBindingClient{
		binding: &workflowrunner.SessionBinding{
			TaskID:    "task-123",
			Resumable: false,
		},
	}
	driver := &fakeTurnAdopter{}
	resolve := func(sessionID string) (*csc.Agent, error) {
		return csc.NewAgentWithEndpoint("http://test"), nil
	}
	adopter := &sessionAdopter{
		bindings: bindings,
		driver:   driver,
		resolve:  resolve,
	}

	adopter.maybeAdopt(context.Background(), "session-789")
	adopter.maybeAdopt(context.Background(), "session-789")

	if bindings.bindingCalls != 1 {
		t.Fatalf("GetSessionBinding calls = %d, want 1", bindings.bindingCalls)
	}
	if bindings.resumeCalls != 0 {
		t.Fatalf("ResumeBeginTask calls = %d, want 0", bindings.resumeCalls)
	}
	if len(driver.calls) != 0 {
		t.Fatalf("AdoptUserTurn calls = %d, want 0", len(driver.calls))
	}
}

func TestMaybeAdoptSilencesAlreadyRunning(t *testing.T) {
	agent := csc.NewAgentWithEndpoint("http://test")
	bindings := &fakeBindingClient{
		binding: &workflowrunner.SessionBinding{
			TaskID:    "task-123",
			Resumable: true,
		},
	}
	driver := &fakeTurnAdopter{err: workflowrunner.ErrTaskAlreadyRunning}
	resolve := func(sessionID string) (*csc.Agent, error) { return agent, nil }
	adopter := &sessionAdopter{
		bindings: bindings,
		driver:   driver,
		resolve:  resolve,
	}

	adopter.maybeAdopt(context.Background(), "session-456")

	if bindings.resumeCalls != 1 {
		t.Fatalf("ResumeBeginTask calls = %d, want 1", bindings.resumeCalls)
	}
	if len(driver.calls) != 1 {
		t.Fatalf("AdoptUserTurn calls = %d, want 1", len(driver.calls))
	}
}

func TestMaybeAdoptResolveFailsBeforeResume(t *testing.T) {
	bindings := &fakeBindingClient{
		binding: &workflowrunner.SessionBinding{
			TaskID:    "task-123",
			Resumable: true,
		},
	}
	driver := &fakeTurnAdopter{}
	resolve := func(sessionID string) (*csc.Agent, error) {
		return nil, errors.New("agent gone")
	}
	adopter := &sessionAdopter{
		bindings: bindings,
		driver:   driver,
		resolve:  resolve,
	}

	adopter.maybeAdopt(context.Background(), "session-456")

	if bindings.resumeCalls != 0 {
		t.Fatalf("ResumeBeginTask calls = %d, want 0", bindings.resumeCalls)
	}
	if len(driver.calls) != 0 {
		t.Fatalf("AdoptUserTurn calls = %d, want 0", len(driver.calls))
	}
}
