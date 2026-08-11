package csc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cs-cloud/internal/agent"
)

func TestRunSessionReturnsTerminalErrorFromEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"error_max_turns\",\"isError\":true}\n\n"))
			_, _ = w.Write([]byte("event: session.error\n"))
			_, _ = w.Write([]byte("data: {\"error\":{\"subtype\":\"error_max_turns\",\"message\":\"Max turns reached\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	_, err := agent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil, "")
	if err == nil {
		t.Fatal("expected terminal session error, got nil")
	}
	if got, want := err.Error(), "wait for completion: Max turns reached"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestRunSessionRejectsCompletedSessionWithoutAssistantOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"success\"}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/message":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cscAgent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	_, err := cscAgent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil, "")
	if !errors.Is(err, agent.ErrEmptySessionOutput) {
		t.Fatalf("RunSession error = %v, want ErrEmptySessionOutput", err)
	}
}

func TestRunSessionWaitsThroughToolUseIdle(t *testing.T) {
	var finalMessageReady atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"success\",\"stopReason\":\"tool_use\"}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
			flusher.Flush()

			time.Sleep(50 * time.Millisecond)

			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"success\",\"stopReason\":\"end_turn\"}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
			finalMessageReady.Store(true)
			flusher.Flush()
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/message":
			w.Header().Set("Content-Type", "application/json")
			if !finalMessageReady.Load() {
				_, _ = w.Write([]byte(`{"messages":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"messages":[
				{"role":"assistant","content":[{
					"type":"text","text":"task finished"
				}]}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cscAgent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	out, err := cscAgent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil, "")
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if got, want := string(out), "task finished"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunSessionReturnsNestedCSCMessageContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"success\"}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/message":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[
				{"role":"assistant","content":{
					"type":"message",
					"role":"assistant",
					"content":[{"type":"text","text":"all fixes applied"}]
				}}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cscAgent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	out, err := cscAgent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil, "")
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if got, want := string(out), "all fixes applied"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestCreateSessionFailsWhenWorkerStopsBeforeReady(t *testing.T) {
	var created bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			if !created {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"session-1","status":"stopped"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"session_id":"session-1","status":"starting","version":"1.0.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter, err := NewAdapterServer(server.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}
	defer func() {
		_ = adapter.Close(context.Background())
	}()

	agent := &Agent{
		endpoint:    adapter.URL(),
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = agent.CreateSession(ctx, "session-1", t.TempDir(), nil, "")
	if err == nil || !strings.Contains(err.Error(), "stopped before becoming ready") {
		t.Fatalf("CreateSession error = %v, want stopped-before-ready error", err)
	}
}

func TestCreateSessionPermissionMode(t *testing.T) {
	cases := []struct {
		name      string
		permMode  string
		customEnv map[string]string
		want      string
	}{
		{"explicit bypass", "bypassPermissions", nil, "bypassPermissions"},
		{"explicit default", "default", nil, "default"},
		{"explicit mode beats env override", "bypassPermissions", map[string]string{"ACP_PERMISSION_MODE": "default"}, "bypassPermissions"},
		{"empty falls back to env bypass", "", map[string]string{"ACP_PERMISSION_MODE": "bypassPermissions"}, "bypassPermissions"},
		{"empty falls back to env default", "", map[string]string{"ACP_PERMISSION_MODE": "default"}, "default"},
		{"empty with unrecognized env falls back to default", "", map[string]string{"ACP_PERMISSION_MODE": "acceptEdits"}, "default"},
		{"empty without env falls back to default", "", nil, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
					http.NotFound(w, r)
				case r.Method == http.MethodPost && r.URL.Path == "/session":
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode /session body: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"session_id":"session-1","status":"running","version":"1.0.0"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			agent := &Agent{
				endpoint:    server.URL,
				rawEndpoint: server.URL,
				httpClient:  server.Client(),
				customEnv:   tc.customEnv,
			}
			if err := agent.CreateSession(context.Background(), "session-1", t.TempDir(), nil, tc.permMode); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if got := body["permission_mode"]; got != tc.want {
				t.Fatalf("permission_mode = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestExtractLastAssistantText(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","parts":[{"type":"text","text":"do it"}]},
		{"role":"assistant","parts":[
			{"type":"reasoning","text":"thinking"},
			{"type":"text","text":"first line"},
			{"type":"text","text":"second line"}
		]}
	]}`)

	out, err := extractLastAssistantText(body)
	if err != nil {
		t.Fatalf("extractLastAssistantText: %v", err)
	}
	if out != "first line\nsecond line" {
		t.Fatalf("out = %q", out)
	}
}

func TestExtractLastAssistantTextFromCSCMessageContent(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"do it"}]},
		{"role":"assistant","content":{
			"id":"message-1",
			"type":"message",
			"role":"assistant",
			"content":[
				{"type":"thinking","thinking":"checking"},
				{"type":"text","text":"all fixes applied"}
			]
		}}
	]}`)

	out, err := extractLastAssistantText(body)
	if err != nil {
		t.Fatalf("extractLastAssistantText: %v", err)
	}
	if out != "all fixes applied" {
		t.Fatalf("out = %q, want %q", out, "all fixes applied")
	}
}

func TestExtractLastAssistantTextFromDirectContent(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"checking"},
			{"type":"text","text":"direct content"}
		]}
	]}`)

	out, err := extractLastAssistantText(body)
	if err != nil {
		t.Fatalf("extractLastAssistantText: %v", err)
	}
	if out != "direct content" {
		t.Fatalf("out = %q, want %q", out, "direct content")
	}
}

func TestExtractLastAssistantTextSkipsAssistantWithoutText(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"assistant","content":{"content":[
			{"type":"text","text":"completed output"}
		]}},
		{"role":"assistant","content":{"content":[
			{"type":"tool_use","name":"verify"}
		]}}
	]}`)

	out, err := extractLastAssistantText(body)
	if err != nil {
		t.Fatalf("extractLastAssistantText: %v", err)
	}
	if out != "completed output" {
		t.Fatalf("out = %q, want %q", out, "completed output")
	}
}

func TestEnvSliceToMap(t *testing.T) {
	got := envSliceToMap([]string{
		"CS_CLOUD_NODE_RUN_ID=old",
		"INVALID",
		"=empty-key",
		"CS_CLOUD_NODE_RUN_ID=nr-1",
		"CS_CLOUD_WORKTREE=C:\\work=tree",
	})

	if got["CS_CLOUD_NODE_RUN_ID"] != "nr-1" {
		t.Fatalf("CS_CLOUD_NODE_RUN_ID = %q, want nr-1", got["CS_CLOUD_NODE_RUN_ID"])
	}
	if got["CS_CLOUD_WORKTREE"] != `C:\work=tree` {
		t.Fatalf("CS_CLOUD_WORKTREE = %q", got["CS_CLOUD_WORKTREE"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty key should be skipped")
	}
}
