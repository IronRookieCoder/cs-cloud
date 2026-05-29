package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/config"
	"cs-cloud/internal/runtime"
)

func TestNewNotifyForwarder(t *testing.T) {
	eventBus := runtime.NewEventBus()
	client := NewClient(nil)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-1", "", 60, 5)
	if f == nil {
		t.Fatal("expected non-nil forwarder")
	}
}

func TestNewNotifyForwarder_DefaultBufferSeconds(t *testing.T) {
	f := NewNotifyForwarder(nil, nil, "dev-1", "token-1", "", 0, 0)
	if f.bufferSeconds != 60 {
		t.Errorf("expected default 60s, got %d", f.bufferSeconds)
	}
	if f.permissionBufferSeconds != 5 {
		t.Errorf("expected default permission 5s, got %d", f.permissionBufferSeconds)
	}
	f2 := NewNotifyForwarder(nil, nil, "dev-1", "token-1", "", -1, -1)
	if f2.bufferSeconds != 60 {
		t.Errorf("expected default 60s for negative, got %d", f2.bufferSeconds)
	}
	if f2.permissionBufferSeconds != 5 {
		t.Errorf("expected default permission 5s for negative, got %d", f2.permissionBufferSeconds)
	}
}

func TestNotifyForwarder_Validate(t *testing.T) {
	tests := []struct {
		name        string
		deviceID    string
		deviceToken string
		wantErr     bool
	}{
		{"valid", "dev-1", "token-1", false},
		{"missing device token", "dev-1", "", true},
		{"missing device ID", "", "token-1", true},
		{"both missing", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewNotifyForwarder(nil, nil, tt.deviceID, tt.deviceToken, "", 60, 5)
			err := f.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestNotifyForwarder_PermissionBatch_Window verifies that multiple permission.asked
// events within the window are sent as a single permission_batch.
func TestNotifyForwarder_PermissionBatch_Window(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	// Send multiple permission.asked within the window
	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "sess-1",
		Data: map[string]any{
			"permissionType": "bash.execute",
			"tool":           map[string]any{"name": "bash"},
		},
	})
	time.Sleep(100 * time.Millisecond)
	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "sess-1",
		Data: map[string]any{
			"permissionType": "write_file",
			"tool":           map[string]any{"name": "write"},
		},
	})

	// Wait for window timeout
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["type"] != "permission_batch" {
		t.Errorf("expected type=permission_batch, got %v", receivedPayload["type"])
	}
	data, ok := receivedPayload["data"].(map[string]any)
	if !ok {
		t.Fatal("expected data to be a map")
	}
	perms, ok := data["permissions"].([]any)
	if !ok {
		t.Fatal("expected permissions to be an array")
	}
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions in batch, got %d", len(perms))
	}
}

// TestNotifyForwarder_PermissionSingle_SendsIndividual verifies that a single
// permission.asked within the window is sent as type=permission (not batch).
func TestNotifyForwarder_PermissionSingle_SendsIndividual(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "sess-1",
		Data: map[string]any{
			"permissionType": "bash.execute",
			"tool":           map[string]any{"name": "bash"},
		},
	})

	// Wait for window timeout
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["type"] != "permission" {
		t.Errorf("expected type=permission for single, got %v", receivedPayload["type"])
	}
}

// TestNotifyForwarder_PermissionBatch_CancelledByResponse verifies that
// permission.replied cancels the entire batch for that session.
func TestNotifyForwarder_PermissionBatch_CancelledByResponse(t *testing.T) {
	var requestCount int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "sess-10",
		Data:           map[string]any{"tool": map[string]any{"name": "bash"}},
	})
	time.Sleep(200 * time.Millisecond)
	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "sess-10",
		Data:           map[string]any{"tool": map[string]any{"name": "write"}},
	})
	time.Sleep(200 * time.Millisecond)

	// Send replied before window timeout → cancels batch
	eventBus.Emit(agent.Event{
		Type:           "permission.replied",
		ConversationID: "sess-10",
	})

	// Wait past window timeout
	time.Sleep(6 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if requestCount != 0 {
		t.Errorf("expected 0 requests (batch cancelled), got %d", requestCount)
	}
}

// TestNotifyForwarder_QuestionBuffer verifies question events still use long buffer.
func TestNotifyForwarder_QuestionBuffer(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 1, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "question.asked",
		ConversationID: "sess-2",
		Data: map[string]any{
			"questions": []any{
				map[string]any{
					"question": "Deploy env?",
					"options":  []any{map[string]any{"label": "dev"}, map[string]any{"label": "prod"}},
				},
			},
		},
	})

	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["type"] != "question" {
		t.Errorf("expected type=question, got %v", receivedPayload["type"])
	}
}

func TestNotifyForwarder_ResponseEvent_NotBuffered(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "permission.replied",
		ConversationID: "sess-3",
	})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a responded payload")
	}
	if receivedPayload["sessionID"] != "sess-3" {
		t.Errorf("expected sessionID=sess-3, got %v", receivedPayload["sessionID"])
	}
	if receivedPayload["type"] != "permission" {
		t.Errorf("expected type=permission, got %v", receivedPayload["type"])
	}
}

func TestNotifyForwarder_IgnoresOtherEvents(t *testing.T) {
	var received bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	for _, eventType := range []string{"session.started", "message.created", "unknown"} {
		eventBus.Emit(agent.Event{
			Type:           eventType,
			ConversationID: "sess-4",
		})
	}

	time.Sleep(200 * time.Millisecond)

	if received {
		t.Error("expected server NOT to receive any request for ignored events")
	}
}

func TestNotifyForwarder_SessionIdle_ImmediateForward(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "session.idle",
		ConversationID: "sess-5",
	})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive idle notification")
	}
	if receivedPayload["type"] != "idle" {
		t.Errorf("expected type=idle, got %v", receivedPayload["type"])
	}
}

func TestNotifyForwarder_ContextCancellation(t *testing.T) {
	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: "http://127.0.0.1:1"}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	cancel()
	time.Sleep(100 * time.Millisecond)

	if eventBus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after cancel, got %d", eventBus.SubscriberCount())
	}
}

func TestBuildPermissionData(t *testing.T) {
	tests := []struct {
		name string
		data any
		want map[string]any
	}{
		{"nil", nil, map[string]any{}},
		{"non-map", "string", map[string]any{"raw": "string"}},
		{"map", map[string]any{"key": "val"}, map[string]any{"key": "val"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPermissionData(tt.data)
			if len(got) != len(tt.want) {
				t.Errorf("buildPermissionData() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("buildPermissionData()[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestBuildQuestionData(t *testing.T) {
	tests := []struct {
		name string
		data any
		want map[string]any
	}{
		{"nil", nil, map[string]any{}},
		{"non-map", 42, map[string]any{"raw": 42}},
		{"map", map[string]any{"q": "a"}, map[string]any{"q": "a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQuestionData(tt.data)
			if len(got) != len(tt.want) {
				t.Errorf("buildQuestionData() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("buildQuestionData()[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestNotifyForwarder_PathResolution(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 1)

	eventBus.RegisterSessionCwd("sess-100", "/home/user/project")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "default",
		Data: map[string]any{
			"session_id": "sess-100",
			"tool":       map[string]any{"name": "bash"},
		},
	})

	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["sessionID"] != "sess-100" {
		t.Errorf("expected sessionID=sess-100, got %v", receivedPayload["sessionID"])
	}
	if receivedPayload["path"] != "/home/user/project" {
		t.Errorf("expected path=/home/user/project, got %v", receivedPayload["path"])
	}
}

func TestNotifyForwarder_ResponseEvent_SessionIDFromData(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "question.replied",
		ConversationID: "default",
		Data: map[string]any{
			"session_id": "sess-200",
		},
	})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a responded payload")
	}
	if receivedPayload["sessionID"] != "sess-200" {
		t.Errorf("expected sessionID=sess-200, got %v", receivedPayload["sessionID"])
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name string
		data any
		want string
	}{
		{"nil", nil, ""},
		{"non-map", "string", ""},
		{"map with session_id", map[string]any{"session_id": "abc-123"}, "abc-123"},
		{"map without session_id", map[string]any{"other": "val"}, ""},
		{"empty session_id", map[string]any{"session_id": ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSessionID(tt.data); got != tt.want {
				t.Errorf("extractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotifyForwarder_ActiveWorkspaceFallback(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 60, 1)

	eventBus.SetActiveWorkspace("/home/user/my-project")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "permission.asked",
		ConversationID: "unknown-session",
		Data: map[string]any{
			"tool": map[string]any{"name": "bash"},
		},
	})

	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["sessionID"] != "unknown-session" {
		t.Errorf("expected sessionID=unknown-session, got %v", receivedPayload["sessionID"])
	}
	if receivedPayload["path"] != "/home/user/my-project" {
		t.Errorf("expected path=/home/user/my-project, got %v", receivedPayload["path"])
	}
}

func TestNotifyForwarder_SessionCwdTakesPriorityOverActive(t *testing.T) {
	var receivedPayload map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eventBus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: server.URL}
	client := NewClient(cfg)
	f := NewNotifyForwarder(eventBus, client, "dev-1", "token-abc", "", 1, 5)

	eventBus.SetActiveWorkspace("/home/user/default")
	eventBus.RegisterSessionCwd("sess-300", "/home/user/specific-project")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	eventBus.Emit(agent.Event{
		Type:           "question.asked",
		ConversationID: "default",
		Data: map[string]any{
			"session_id": "sess-300",
		},
	})

	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if receivedPayload == nil {
		t.Fatal("expected server to receive a notification payload")
	}
	if receivedPayload["path"] != "/home/user/specific-project" {
		t.Errorf("expected session-specific path=/home/user/specific-project, got %v", receivedPayload["path"])
	}
}

func TestIsResponseEvent(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{"permission.replied", true},
		{"question.replied", true},
		{"question.rejected", true},
		{"permission.asked", false},
		{"session.idle", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			if got := isResponseEvent(tt.eventType); got != tt.want {
				t.Errorf("isResponseEvent(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}
