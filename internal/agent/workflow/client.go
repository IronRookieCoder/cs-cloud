package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

// Client is a REST client for the multica backend.
type Client struct {
	baseURL       string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

// StatusError carries the HTTP status of a failed multica call so callers
// can react to specific codes (e.g. 404 → re-register).
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// ErrRuntimeGone is returned (wrapped) when multica reports the runtime row
// no longer exists; the driver should re-register.
var ErrRuntimeGone = errors.New("runtime gone")

// NewClient creates a new multica REST client.
func NewClient(baseURL string, tp func() (*provider.Credentials, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tp,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) token() (string, error) {
	cred, err := c.tokenProvider()
	if err != nil {
		return "", err
	}
	if cred == nil || cred.AccessToken == "" {
		return "", fmt.Errorf("no access token")
	}
	return cred.AccessToken, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	token, err := c.token()
	if err != nil {
		return err
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workflow.HeaderClientPlatform, "cs-cloud")
	req.Header.Set(workflow.HeaderClientVersion, "dev")
	req.Header.Set(workflow.HeaderClientOS, runtime.GOOS)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return &StatusError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(b)}
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GetWorkspaces fetches all workspaces from multica.
func (c *Client) GetWorkspaces(ctx context.Context) ([]workflow.Workspace, error) {
	var out []workflow.Workspace
	err := c.request(ctx, http.MethodGet, workflow.MulticaWorkspacesEndpoint, nil, &out)
	return out, err
}

// GetProjects fetches all projects for a workspace from multica.
func (c *Client) GetProjects(ctx context.Context, workspaceID string) ([]workflow.Project, error) {
	var out []workflow.Project
	path := fmt.Sprintf(workflow.MulticaProjectsEndpoint, workspaceID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// StartTask marks a task as started.
func (c *Client) StartTask(ctx context.Context, taskID string) error {
	path := fmt.Sprintf(workflow.MulticaTaskStartEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, nil, nil)
}

// CompleteTask marks a task as complete with the given result.
func (c *Client) CompleteTask(ctx context.Context, taskID string, result any) error {
	path := fmt.Sprintf(workflow.MulticaTaskCompleteEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"result": result}, nil)
}

// FailTask marks a task as failed with the given reason.
func (c *Client) FailTask(ctx context.Context, taskID string, reason string) error {
	path := fmt.Sprintf(workflow.MulticaTaskFailEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"error": reason}, nil)
}

// PostTaskMessages uploads task output messages.
func (c *Client) PostTaskMessages(ctx context.Context, taskID string, messages string) error {
	path := fmt.Sprintf(workflow.MulticaTaskMessagesEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"messages": messages}, nil)
}

// RegisterDaemon registers this device as a cs-cloud runtime in the given
// workspace and returns the registered runtime rows (with their IDs).
func (c *Client) RegisterDaemon(ctx context.Context, req workflow.DaemonRegisterRequest) ([]workflow.DaemonRuntimeResponse, error) {
	var out workflow.DaemonRegisterResponse
	err := c.request(ctx, http.MethodPost, workflow.MulticaDaemonRegisterEndpoint, req, &out)
	return out.Runtimes, err
}

// Heartbeat keeps a registered runtime alive. It returns ErrRuntimeGone
// (wrapped) when multica responds 404, meaning the row was deleted and the
// caller should re-register.
func (c *Client) Heartbeat(ctx context.Context, runtimeID string) error {
	err := c.request(ctx, http.MethodPost, workflow.MulticaDaemonHeartbeatEndpoint, map[string]any{"runtime_id": runtimeID}, nil)
	if err != nil {
		var stErr *StatusError
		if errors.As(err, &stErr) && stErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrRuntimeGone, err)
		}
	}
	return err
}

// DeregisterDaemon removes the given runtime rows (best-effort shutdown).
func (c *Client) DeregisterDaemon(ctx context.Context, runtimeIDs []string) error {
	return c.request(ctx, http.MethodPost, workflow.MulticaDaemonDeregisterEndpoint, map[string]any{"runtime_ids": runtimeIDs}, nil)
}
