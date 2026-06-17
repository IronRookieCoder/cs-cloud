package localserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/device"
)

type mockReconnecter struct {
	called bool
	mu     sync.Mutex
}

func (m *mockReconnecter) Reconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
}

func (m *mockReconnecter) wasCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called
}

func TestDispatcherAcceptsCommand(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	ack, err := d.Dispatch(context.Background(), &commandRequest{
		CommandID: "cmd-001",
		Type:      "restart",
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if ack.CommandID != "cmd-001" {
		t.Errorf("command_id=%q, want cmd-001", ack.CommandID)
	}
	if ack.Status != "accepted" {
		t.Errorf("status=%q, want accepted", ack.Status)
	}
}

func TestDispatcherRejectsDuplicateCommandID(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	d.BindRestarter(func() { time.Sleep(2 * time.Second) })

	_, err := d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-dup", Type: "restart"})
	if err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	_, err = d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-dup", Type: "restart"})
	if err == nil {
		t.Fatal("expected error for duplicate command_id")
	}
}

func TestDispatcherRejectsWhenExecuting(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	block := make(chan struct{})
	d.BindTunnel(&blockingReconnecter{block: block})

	_, err := d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-1", Type: "reconnect"})
	if err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	_, err = d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-2", Type: "reconnect"})
	if err == nil {
		t.Fatal("expected error when command already executing")
	}

	close(block)
	time.Sleep(100 * time.Millisecond)
}

type blockingReconnecter struct {
	block chan struct{}
}

func (b *blockingReconnecter) Reconnect() {
	<-b.block
}

func TestDispatcherStatusNotFound(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	_, err := d.Status("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestDispatcherReconnectCommand(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	mock := &mockReconnecter{}
	d.BindTunnel(mock)

	ack, err := d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-recon", Type: "reconnect"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if ack.Status != "accepted" {
		t.Fatalf("status=%q", ack.Status)
	}

	time.Sleep(200 * time.Millisecond)

	if !mock.wasCalled() {
		t.Error("expected reconnect to be called")
	}

	status, err := d.Status("cmd-recon")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("status=%q, want completed", status.Status)
	}
}

func TestDispatcherRestartCommand(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	restarted := false
	var mu sync.Mutex
	d.BindRestarter(func() {
		mu.Lock()
		restarted = true
		mu.Unlock()
	})

	ack, _ := d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-restart", Type: "restart"})
	if ack.Status != "accepted" {
		t.Fatalf("status=%q", ack.Status)
	}

	time.Sleep(2 * time.Second)

	mu.Lock()
	if !restarted {
		t.Error("expected restarter to be called")
	}
	mu.Unlock()

	status, _ := d.Status("cmd-restart")
	if status.Status != "completed" {
		t.Errorf("status=%q, want completed", status.Status)
	}
}

func TestDispatcherUpgradeNoUpdater(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-upgrade", Type: "upgrade"})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-upgrade")
	if status.Status != "failed" {
		t.Errorf("status=%q, want failed", status.Status)
	}
	if status.Error == "" {
		t.Error("expected error message")
	}
}

func TestDispatcherUnknownCommandType(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-unknown", Type: "bogus"})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-unknown")
	if status.Status != "failed" {
		t.Errorf("status=%q, want failed", status.Status)
	}
}

func TestDispatcherCleanup(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.active["old-cmd"] = &commandEntry{
		req:         &commandRequest{CommandID: "old-cmd", Type: "reconnect"},
		status:      "completed",
		completedAt: time.Now().Add(-2 * time.Hour),
	}

	d.Cleanup(1 * time.Hour)

	if _, exists := d.active["old-cmd"]; exists {
		t.Error("old command should be cleaned up")
	}
}

func TestDispatcherHandleHeartbeatCommands(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	mock := &mockReconnecter{}
	d.BindTunnel(mock)

	cmds := []device.CloudCommand{
		{CommandID: "hb-1", Type: "reconnect"},
	}
	d.HandleHeartbeatCommands(cmds)

	time.Sleep(200 * time.Millisecond)

	if !mock.wasCalled() {
		t.Error("expected reconnect from heartbeat command")
	}

	status, err := d.Status("hb-1")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("status=%q, want completed", status.Status)
	}
}

func TestDispatcherHeartbeatRejectsDuplicate(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	mock := &mockReconnecter{}
	d.BindTunnel(mock)

	d.Dispatch(context.Background(), &commandRequest{CommandID: "hb-dup", Type: "reconnect"})
	time.Sleep(100 * time.Millisecond)

	cmds := []device.CloudCommand{
		{CommandID: "hb-dup", Type: "reconnect"},
	}
	d.HandleHeartbeatCommands(cmds)

	time.Sleep(100 * time.Millisecond)

	if !mock.wasCalled() {
		t.Error("expected at least one reconnect call")
	}
}

func TestDispatcherStatusReturnsTiming(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)
	mock := &mockReconnecter{}
	d.BindTunnel(mock)

	d.Dispatch(context.Background(), &commandRequest{CommandID: "cmd-timing", Type: "reconnect"})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-timing")
	if status.StartedAt == "" {
		t.Error("expected started_at to be set")
	}
	if status.CompletedAt == "" {
		t.Error("expected completed_at to be set")
	}
}

func TestDispatcherRestartAgentCommand(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	restarted := false
	var mu sync.Mutex
	d.BindAgentRestarter(func(ctx context.Context, onProgress func(phase string, progress float64, message string)) error {
		mu.Lock()
		restarted = true
		mu.Unlock()
		onProgress("killing", 0.1, "Stopping agents")
		onProgress("killed", 0.3, "Stopped")
		onProgress("detecting", 0.4, "Detecting binary")
		onProgress("ready", 1.0, "Done")
		return nil
	})

	ack, err := d.Dispatch(context.Background(), &commandRequest{
		CommandID: "cmd-restart-agent",
		Type:      "restart-agent",
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if ack.Status != "accepted" {
		t.Fatalf("status=%q, want accepted", ack.Status)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if !restarted {
		t.Error("expected agent restarter to be called")
	}
	mu.Unlock()

	status, err := d.Status("cmd-restart-agent")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("status=%q, want completed", status.Status)
	}
}

func TestDispatcherRestartAgentNoRestarter(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.Dispatch(context.Background(), &commandRequest{
		CommandID: "cmd-no-restarter",
		Type:      "restart-agent",
	})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-no-restarter")
	if status.Status != "failed" {
		t.Errorf("status=%q, want failed", status.Status)
	}
	if status.Error == "" {
		t.Error("expected error message when agent restarter not bound")
	}
}

func TestDispatcherRestartAgentProgress(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.BindAgentRestarter(func(ctx context.Context, onProgress func(phase string, progress float64, message string)) error {
		onProgress("killing", 0.1, "Stopping agents")
		onProgress("killed", 0.3, "Stopped")
		onProgress("detecting", 0.4, "Detecting binary")
		onProgress("ready", 1.0, "Done")
		return nil
	})

	d.Dispatch(context.Background(), &commandRequest{
		CommandID: "cmd-progress",
		Type:      "restart-agent",
	})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-progress")
	if status.Status != "completed" {
		t.Fatalf("status=%q, want completed", status.Status)
	}
	if status.Phase != "ready" {
		t.Errorf("phase=%q, want ready", status.Phase)
	}
	if status.Progress != 1.0 {
		t.Errorf("progress=%f, want 1.0", status.Progress)
	}
	if status.Message != "Done" {
		t.Errorf("message=%q, want Done", status.Message)
	}
}

func TestDispatcherRestartAgentError(t *testing.T) {
	d := NewCommandDispatcher(nil, nil)

	d.BindAgentRestarter(func(ctx context.Context, onProgress func(phase string, progress float64, message string)) error {
		onProgress("killing", 0.1, "Starting")
		onProgress("failed", 0.0, "something went wrong")
		return errors.New("simulated failure")
	})

	d.Dispatch(context.Background(), &commandRequest{
		CommandID: "cmd-restart-agent-fail",
		Type:      "restart-agent",
	})
	time.Sleep(200 * time.Millisecond)

	status, _ := d.Status("cmd-restart-agent-fail")
	if status.Status != "failed" {
		t.Errorf("status=%q, want failed", status.Status)
	}
	if status.Error != "simulated failure" {
		t.Errorf("error=%q, want simulated failure", status.Error)
	}
}
