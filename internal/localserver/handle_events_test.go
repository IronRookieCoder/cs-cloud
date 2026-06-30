package localserver

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cs-cloud/internal/agent"
)

func TestIsHostEvent(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		want      bool
	}{
		{"git branch changed", "host.git.branch.changed", true},
		{"git commit", "host.git.commit", true},
		{"git status changed", "host.git.status.changed", true},
		{"git remote changed", "host.git.remote.changed", true},
		{"git stash changed", "host.git.stash.changed", true},
		{"file created", "host.file.created", true},
		{"file updated", "host.file.updated", true},
		{"file deleted", "host.file.deleted", true},
		{"file renamed", "host.file.renamed", true},
		{"host bare prefix", "host.", true},
		{"agent runtime restarted", "agent.runtime.restarted", false},
		{"permission asked", "permission.asked", false},
		{"question asked", "question.asked", false},
		{"session idle", "session.idle", false},
		{"empty string", "", false},
		{"unrelated", "some.other.event", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isHostEvent(tc.eventType)
			if got != tc.want {
				t.Errorf("isHostEvent(%q) = %v, want %v", tc.eventType, got, tc.want)
			}
		})
	}
}

func TestIsSystemEvent(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		want      bool
	}{
		{"agent runtime restarted", "agent.runtime.restarted", true},
		{"git branch changed not system", "host.git.branch.changed", false},
		{"git stash changed not system", "host.git.stash.changed", false},
		{"file created not system", "host.file.created", false},
		{"permission asked not system", "permission.asked", false},
		{"empty string", "", false},
		{"other agent runtime", "agent.runtime.something", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSystemEvent(tc.eventType)
			if got != tc.want {
				t.Errorf("isSystemEvent(%q) = %v, want %v", tc.eventType, got, tc.want)
			}
		})
	}
}

func TestShouldSendEventToWorkspace(t *testing.T) {
	workspace := "/tmp/my-workspace"
	workspace = filepath.Clean(workspace)

	tests := []struct {
		name      string
		event     agent.Event
		workspace string
		want      bool
	}{
		{
			name:      "git event with matching repo_path",
			event:     agent.Event{Type: "host.git.commit", Data: map[string]any{"repo_path": workspace}},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "git event with mismatched repo_path",
			event:     agent.Event{Type: "host.git.commit", Data: map[string]any{"repo_path": "/tmp/other-repo"}},
			workspace: workspace,
			want:      false,
		},
		{
			name:      "git event with empty repo_path falls through to allow",
			event:     agent.Event{Type: "host.git.commit", Data: map[string]any{"repo_path": ""}},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "git event without repo_path falls through to allow",
			event:     agent.Event{Type: "host.git.commit", Data: map[string]any{"branch": "main"}},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "file event within workspace",
			event:     agent.Event{Type: "host.file.updated", Data: map[string]any{"path": workspace + "/subdir/file.txt"}},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "file event outside workspace",
			event:     agent.Event{Type: "host.file.updated", Data: map[string]any{"path": "/tmp/other-dir/file.txt"}},
			workspace: workspace,
			want:      false,
		},
		{
			name:      "event with nil data allowed",
			event:     agent.Event{Type: "host.git.commit", Data: nil},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "event with non-map data allowed",
			event:     agent.Event{Type: "host.git.commit", Data: "raw string"},
			workspace: workspace,
			want:      true,
		},
		{
			name:      "git stash event with matching repo_path",
			event:     agent.Event{Type: "host.git.stash.changed", Data: map[string]any{"repo_path": workspace, "count": 1}},
			workspace: workspace,
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSendEventToWorkspace(tc.event, tc.workspace)
			if got != tc.want {
				t.Errorf("shouldSendEventToWorkspace() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSerializeHostEvent(t *testing.T) {
	evt := agent.Event{
		Type: "host.git.stash.changed",
		Data: map[string]any{
			"branch":     "main",
			"count":      2,
			"repo_path":  "/tmp/repo",
		},
	}

	out := serializeHostEvent(evt)
	if out == "" {
		t.Fatal("Expected non-empty serialization output")
	}

	var envelope struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("Output is not valid JSON: %v\nOutput: %s", err, out)
	}

	if envelope.Type != "host.git.stash.changed" {
		t.Errorf("Expected type 'host.git.stash.changed', got %q", envelope.Type)
	}

	if envelope.Properties == nil {
		t.Fatal("Expected non-nil properties map")
	}

	// Original data fields must be present
	if branch, ok := envelope.Properties["branch"].(string); !ok || branch != "main" {
		t.Errorf("Expected properties.branch == 'main', got %v", envelope.Properties["branch"])
	}
	if count, ok := envelope.Properties["count"].(float64); !ok || count != 2 {
		t.Errorf("Expected properties.count == 2, got %v", envelope.Properties["count"])
	}
	if repo, ok := envelope.Properties["repo_path"].(string); !ok || repo != "/tmp/repo" {
		t.Errorf("Expected properties.repo_path == '/tmp/repo', got %v", envelope.Properties["repo_path"])
	}

	// A timestamp must be added by the serializer
	if _, ok := envelope.Properties["timestamp"]; !ok {
		t.Error("Expected properties.timestamp to be set by serializer")
	}

	// Output must match the documented SSE envelope shape
	if !strings.HasPrefix(out, "{\"type\":") {
		t.Errorf("Expected JSON to start with {\"type\":, got: %s", out[:min(len(out), 30)])
	}
}

func TestSerializeHostEvent_NilData(t *testing.T) {
	evt := agent.Event{Type: "host.git.branch.changed", Data: nil}
	out := serializeHostEvent(evt)
	if out == "" {
		t.Fatal("Expected non-empty output for nil data")
	}

	var envelope struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("Output is not valid JSON: %v", err)
	}
	if envelope.Type != "host.git.branch.changed" {
		t.Errorf("Expected type 'host.git.branch.changed', got %q", envelope.Type)
	}
	if _, ok := envelope.Properties["timestamp"]; !ok {
		t.Error("Expected timestamp to be present even with nil data")
	}
}

func TestSerializeHostEvent_NonMapData(t *testing.T) {
	// Non-map data should not cause a panic; properties will only contain timestamp.
	evt := agent.Event{Type: "host.git.commit", Data: "raw string"}
	out := serializeHostEvent(evt)
	if out == "" {
		t.Fatal("Expected non-empty output for non-map data")
	}

	var envelope struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("Output is not valid JSON: %v", err)
	}
	if envelope.Type != "host.git.commit" {
		t.Errorf("Expected type 'host.git.commit', got %q", envelope.Type)
	}
}
