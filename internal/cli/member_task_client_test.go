package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"cs-cloud/internal/device"
	"cs-cloud/internal/membertask"
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

func TestMemberTaskClientPreservesDeviceRegistrationRequired(t *testing.T) {
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error {
			return device.ErrRegistrationRequired
		},
	}
	_, err := client.Execute(context.Background(), memberTaskRequest{Command: "list"})
	taskErr, ok := err.(*membertask.TaskError)
	if !ok || taskErr.Code != "device_registration_required" {
		t.Fatalf("error = %#v, want device_registration_required", err)
	}
	if strings.Contains(taskErr.Message, "cs-cloud") || strings.Contains(taskErr.Message, "register") || !strings.Contains(taskErr.Message, "csc cloud start") {
		t.Fatalf("message = %q, want csc cloud start guidance", taskErr.Message)
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

func TestMemberTaskClientDoesNotDecodeResponsePairedWithTransportError(t *testing.T) {
	var reads atomic.Int32
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error { return nil },
		resolve:      func() (string, string, error) { return "http://127.0.0.1:9876", "secret", nil },
		do: func(req *http.Request) (*http.Response, error) {
			body := &countingReadCloser{reader: strings.NewReader(`{"ok":true,"data":[]}`), reads: &reads}
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, errors.New("transport failed after headers")
		},
	}
	_, err := client.Execute(context.Background(), memberTaskRequest{Command: "list"})
	var taskErr *membertask.TaskError
	if !errors.As(err, &taskErr) || taskErr.Code != "local_transport_unavailable" {
		t.Fatalf("Execute error = %#v", err)
	}
	if reads.Load() != 0 {
		t.Fatalf("response body read %d times despite transport error", reads.Load())
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

func TestMemberTaskClientDecodesPrepareTransitionFacts(t *testing.T) {
	client := &memberTaskClient{
		ensureDaemon: func(context.Context) error { return nil },
		resolve:      func() (string, string, error) { return "http://127.0.0.1:9876", "secret", nil },
		do: func(req *http.Request) (*http.Response, error) {
			return taskHTTPResponse(http.StatusOK, `{"ok":true,"data":{"prepared":true,"outcome":"already_completed","performed":false}}`), nil
		},
	}
	result, err := client.Execute(context.Background(), memberTaskRequest{Command: "prepare", TaskKey: "cloud/ws/node/worker"})
	transition, ok := result.(membertask.LocalTransition)
	if err != nil || !ok || transition.Outcome != membertask.OutcomeAlreadyCompleted || transition.Performed {
		t.Fatalf("result=%+v (%T), err=%v", result, result, err)
	}
}

func taskHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type countingReadCloser struct {
	reader io.Reader
	reads  *atomic.Int32
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(p)
}

func (r *countingReadCloser) Close() error { return nil }
