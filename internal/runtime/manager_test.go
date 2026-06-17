package runtime

import (
	"context"
	"testing"
)

func TestRestartDefaultAgent(t *testing.T) {
	m := NewAgentManager(NewEventBus())

	var phases []string
	var progresses []float64
	var messages []string

	onProgress := func(phase string, progress float64, message string) {
		phases = append(phases, phase)
		progresses = append(progresses, progress)
		messages = append(messages, message)
	}

	err := m.RestartDefaultAgent(
		context.Background(),
		"cs", "", "", "", nil,
		onProgress,
	)

	// Verify progress callbacks were called in correct order
	if len(phases) < 3 {
		t.Fatalf("expected at least 3 progress updates, got %d: %v", len(phases), phases)
	}

	if phases[0] != "killing" {
		t.Errorf("phases[0]=%q, want killing", phases[0])
	}
	if progresses[0] != 0.1 {
		t.Errorf("progresses[0]=%f, want 0.1", progresses[0])
	}
	if phases[1] != "killed" {
		t.Errorf("phases[1]=%q, want killed", phases[1])
	}
	if progresses[1] != 0.3 {
		t.Errorf("progresses[1]=%f, want 0.3", progresses[1])
	}
	if phases[2] != "detecting" {
		t.Errorf("phases[2]=%q, want detecting", phases[2])
	}
	if progresses[2] != 0.4 {
		t.Errorf("progresses[2]=%f, want 0.4", progresses[2])
	}

	if err != nil {
		// InitDefaultAgent may fail in environments without the agent binary
		lastPhase := phases[len(phases)-1]
		if lastPhase != "failed" {
			t.Errorf("last phase=%q, want failed when error occurs", lastPhase)
		}
		if progresses[len(progresses)-1] != 0.0 {
			t.Errorf("last progress=%f, want 0.0", progresses[len(progresses)-1])
		}
	} else {
		// Success case — clean up the agent that was started
		lastPhase := phases[len(phases)-1]
		if lastPhase != "ready" {
			t.Errorf("last phase=%q, want ready", lastPhase)
		}
		if progresses[len(progresses)-1] != 1.0 {
			t.Errorf("last progress=%f, want 1.0", progresses[len(progresses)-1])
		}
		m.KillAll()
	}
}

func TestRestartDefaultAgentNilProgress(t *testing.T) {
	m := NewAgentManager(NewEventBus())

	// Should not panic with nil progress callback
	err := m.RestartDefaultAgent(
		context.Background(),
		"cs", "", "", "", nil,
		nil,
	)

	// Clean up if agent was started
	m.KillAll()
	_ = err
}

func TestRestartDefaultAgentNonNilMessage(t *testing.T) {
	m := NewAgentManager(NewEventBus())

	var messages []string
	onProgress := func(phase string, progress float64, message string) {
		messages = append(messages, message)
	}

	m.RestartDefaultAgent(
		context.Background(),
		"cs", "", "", "", nil,
		onProgress,
	)

	// Verify meaningful messages were provided
	if len(messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(messages))
	}
	if messages[0] == "" {
		t.Error("expected non-empty progress message")
	}
}
