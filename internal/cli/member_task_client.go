package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/membertask"
	"cs-cloud/internal/platform"
)

type memberTaskClient struct {
	ensureDaemon func(context.Context) error
	resolve      func() (string, string, error)
	do           func(*http.Request) (*http.Response, error)
}

func newMemberTaskClient(a *app.App) *memberTaskClient {
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	return &memberTaskClient{
		ensureDaemon: func(ctx context.Context) error { return ensureMemberTaskDaemon(ctx, a) },
		resolve: func() (string, string, error) {
			serverURL, err := a.ServerURL()
			if err != nil {
				return "", "", err
			}
			secret, err := a.MemberTaskSecret()
			return serverURL, secret, err
		},
		do: httpClient.Do,
	}
}

func (c *memberTaskClient) Execute(ctx context.Context, request memberTaskRequest) (any, error) {
	if err := c.ensureDaemon(ctx); err != nil {
		var taskErr *membertask.TaskError
		if errors.As(err, &taskErr) {
			return nil, taskErr
		}
		if errors.Is(err, device.ErrRegistrationRequired) {
			return nil, &membertask.TaskError{Code: "device_registration_required", Message: "device registration is required; run 'csc cloud start' first"}
		}
		return nil, &membertask.TaskError{Code: "daemon_unavailable", Message: "member task daemon is unavailable"}
	}
	serverURL, secret, err := c.resolve()
	if err != nil || serverURL == "" || secret == "" {
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private member task transport is unavailable"}
	}
	base, err := url.Parse(serverURL)
	if err != nil || base.Scheme != "http" || !isLoopbackHost(base.Hostname()) {
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private member task server URL is invalid"}
	}
	method, endpoint, body, err := memberTaskHTTPRequest(request)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + endpoint
	attempts := 1
	if retryableMemberTaskRequest(request) {
		attempts = 3
	}
	var response *http.Response
	var requestErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		var reader io.Reader
		if len(body) > 0 {
			reader = bytes.NewReader(body)
		}
		httpRequest, buildErr := http.NewRequestWithContext(ctx, method, base.String(), reader)
		if buildErr != nil {
			return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "cannot build private task request"}
		}
		httpRequest.Header.Set("Authorization", "Bearer "+secret)
		httpRequest.Header.Set("Accept", "application/json")
		if len(body) > 0 {
			httpRequest.Header.Set("Content-Type", "application/json")
		}
		response, requestErr = c.do(httpRequest)
		if requestErr == nil && (response.StatusCode < 500 || attempt == attempts) {
			break
		}
		if response != nil {
			_ = response.Body.Close()
			response = nil
		}
		if attempt < attempts {
			select {
			case <-ctx.Done():
				return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private task request was canceled"}
			case <-time.After(time.Duration(attempt) * 25 * time.Millisecond):
			}
		}
	}
	if requestErr != nil || response == nil {
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private member task request failed"}
	}
	defer response.Body.Close()
	return decodeMemberTaskResponse(response, request)
}

func memberTaskHTTPRequest(request memberTaskRequest) (string, string, []byte, error) {
	if request.Command == "list" {
		return http.MethodGet, "/api/v1/member-tasks", nil, nil
	}
	key, err := membertask.ParseTaskKey(request.TaskKey)
	if err != nil {
		return "", "", nil, &membertask.TaskError{Code: "invalid_task_key", Message: "task key is invalid"}
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(key.String()))
	if request.Command == "get" {
		return http.MethodGet, "/api/v1/member-tasks/" + encoded, nil, nil
	}
	body := map[string]any{}
	if request.PreviewID != "" {
		body["preview_id"] = request.PreviewID
	}
	if request.Decision != "" {
		body["decision"] = request.Decision
	}
	if request.Reason != "" {
		body["reason"] = request.Reason
	}
	if request.Command == "delete" && request.PreviewID == "" {
		body["mode"] = request.DeleteMode
	}
	if request.Command == "handle" && request.WorkDir != "" {
		body["workdir"] = request.WorkDir
	}
	if len(request.DeliverableFiles) > 0 {
		body["deliverable_files"] = request.DeliverableFiles
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", "", nil, &membertask.TaskError{Code: "invalid_arguments", Message: "cannot encode task request"}
	}
	return http.MethodPost, "/api/v1/member-tasks/" + encoded + "/" + request.Command, payload, nil
}

func retryableMemberTaskRequest(request memberTaskRequest) bool {
	if request.Command == "list" || request.Command == "get" || request.Command == "handle" || request.Command == "recover" {
		return true
	}
	return request.PreviewID != ""
}

func decodeMemberTaskResponse(response *http.Response, request memberTaskRequest) (any, error) {
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private task response is invalid"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		if envelope.Error != nil && envelope.Error.Code != "" {
			return nil, &membertask.TaskError{Code: envelope.Error.Code, Message: envelope.Error.Message}
		}
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: fmt.Sprintf("private task request failed with status %d", response.StatusCode)}
	}
	var target any
	switch request.Command {
	case "list":
		target = &[]membertask.Task{}
	case "get":
		target = &membertask.Task{}
	case "handle":
		target = &membertask.LocalTransition{}
	case "submit", "review":
		if request.PreviewID == "" {
			target = &membertask.Preview{}
		} else {
			target = &membertask.Operation{}
		}
	case "delete":
		if request.PreviewID == "" {
			target = &membertask.DeletePreview{}
		} else {
			var deleted map[string]bool
			target = &deleted
		}
	case "recover":
		target = &membertask.Operation{}
	default:
		return nil, &membertask.TaskError{Code: "invalid_arguments", Message: "unknown task command"}
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private task data is invalid"}
	}
	switch value := target.(type) {
	case *[]membertask.Task:
		return *value, nil
	case *membertask.Task:
		return *value, nil
	case *membertask.LocalTransition:
		return *value, nil
	case *membertask.Preview:
		return *value, nil
	case *membertask.Operation:
		return *value, nil
	case *membertask.DeletePreview:
		return *value, nil
	case *map[string]bool:
		return *value, nil
	default:
		return nil, &membertask.TaskError{Code: "local_transport_unavailable", Message: "private task data type is invalid"}
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ensureMemberTaskDaemon(ctx context.Context, a *app.App) error {
	if running, _, _ := a.DaemonStatus(); running {
		return nil
	}
	a.ForceCleanupStale()
	if err := a.SaveMode("cloud"); err != nil {
		return err
	}
	if _, err := a.PrepareCloudDaemon(ctx); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	nullFile, err := openNullDevice()
	if err != nil {
		return err
	}
	defer nullFile.Close()
	args := []string{"_daemon", "--host", "127.0.0.1"}
	if path := platform.AuthPath(); path != "" {
		args = append(args, "--auth-path", path)
	}
	if dir := platform.DataDir(); dir != "" {
		args = append(args, "--data-dir", dir)
	}
	cmd := newDaemonCmd(executable, args)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nullFile, nullFile, nullFile
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			if err == nil {
				err = errors.New("daemon exited before becoming ready")
			}
			return err
		case <-deadline.C:
			return errors.New("daemon readiness timeout")
		case <-ticker.C:
			if a.MemberTaskDaemonReady() {
				return nil
			}
		}
	}
}
