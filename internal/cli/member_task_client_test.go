package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMemberTaskClientEnsuresDaemonOnceAndUsesPrivateSecret(t *testing.T) {
	var ensureCalls atomic.Int32
	var requests atomic.Int32
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error { ensureCalls.Add(1); return nil },
		resolve:      func() (string, string, error) { return "http://127.0.0.1:9876", "private-secret", nil },
		do: func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			if req.Header.Get("Authorization") != "Bearer private-secret" {
				t.Errorf("authorization = %q", req.Header.Get("Authorization"))
			}
			return taskHTTPResponse(http.StatusOK, `{"ok":true,"data":[]}`), nil
		},
	}
	_, err := client.Execute(context.Background(), memberTaskRequest{Command: "list"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ensureCalls.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("ensure=%d requests=%d", ensureCalls.Load(), requests.Load())
	}
}

func TestMemberTaskClientRetriesTransientReadAtMostThreeTimes(t *testing.T) {
	var attempts atomic.Int32
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error { return nil },
		resolve:      func() (string, string, error) { return "http://127.0.0.1:9876", "secret", nil },
		do: func(req *http.Request) (*http.Response, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("connection reset")
			}
			return taskHTTPResponse(http.StatusOK, `{"ok":true,"data":[]}`), nil
		},
	}
	if _, err := client.Execute(context.Background(), memberTaskRequest{Command: "list"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestTaskConfirmRecoveryRetriesSamePreviewAfterResponseLoss(t *testing.T) {
	var attempts atomic.Int32
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error { return nil },
		resolve:      func() (string, string, error) { return "http://127.0.0.1:9876", "secret", nil },
		do: func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"preview_id":"preview-1"`) {
				t.Errorf("body = %s", body)
			}
			if attempts.Add(1) == 1 {
				return nil, errors.New("response lost")
			}
			return taskHTTPResponse(http.StatusOK, `{"ok":true,"data":{"id":"operation-1","preview_id":"preview-1","status":"completed"}}`), nil
		},
	}
	result, err := client.Execute(context.Background(), memberTaskRequest{Command: "submit", TaskKey: "cloud/ws/node/worker", PreviewID: "preview-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if attempts.Load() != 2 || result == nil {
		t.Fatalf("attempts=%d result=%+v", attempts.Load(), result)
	}
}

func taskHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
