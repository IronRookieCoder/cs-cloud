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
	userBaseURL   string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

// StatusError carries the HTTP status of a failed multica call so callers
// can react to specific codes (e.g. 404 → re-register).
type StatusError struct {
	Method     string
	URL        string
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// ErrRuntimeGone is returned (wrapped) when multica reports the runtime row
// no longer exists; the driver should re-register.
var ErrRuntimeGone = errors.New("runtime gone")

// NewClient creates a new multica REST client.
// baseURL is the daemon API root (typically .../workflow-backend).
// userBaseURL is the user-facing API root used for endpoints such as chat
// sessions; when empty it falls back to baseURL.
func NewClient(baseURL, userBaseURL string, tp func() (*provider.Credentials, error)) *Client {
	if userBaseURL == "" {
		userBaseURL = baseURL
	}
	return &Client{
		baseURL:       baseURL,
		userBaseURL:   userBaseURL,
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
	return c.doRequest(ctx, c.baseURL, method, path, nil, body, out)
}

func (c *Client) userRequest(ctx context.Context, method, path string, body, out any) error {
	return c.doRequest(ctx, c.userBaseURL, method, path, nil, body, out)
}

func (c *Client) doRequest(ctx context.Context, baseURL, method, path string, headers http.Header, body, out any) error {
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

	url := baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workflow.HeaderClientPlatform, "cs-cloud")
	req.Header.Set(workflow.HeaderClientVersion, "dev")
	req.Header.Set(workflow.HeaderClientOS, runtime.GOOS)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return &StatusError{Method: method, URL: url, Path: path, StatusCode: resp.StatusCode, Body: string(b)}
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

// ListIssues fetches issues for a workspace.
func (c *Client) ListIssues(ctx context.Context, workspaceID string) ([]workflow.Issue, error) {
	var out []workflow.Issue
	path := fmt.Sprintf(workflow.MulticaIssuesEndpoint, workspaceID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// CreateIssue creates a new issue in the workspace.
func (c *Client) CreateIssue(ctx context.Context, workspaceID, title, description string) (workflow.Issue, error) {
	var out workflow.Issue
	path := fmt.Sprintf(workflow.MulticaIssuesEndpoint, workspaceID)
	err := c.request(ctx, http.MethodPost, path, map[string]string{
		"title":       title,
		"description": description,
	}, &out)
	return out, err
}

// UpdateIssueStatus updates an issue's status.
func (c *Client) UpdateIssueStatus(ctx context.Context, workspaceID, issueID, status string) error {
	path := fmt.Sprintf(workflow.MulticaIssueEndpoint, workspaceID, issueID)
	return c.request(ctx, http.MethodPut, path, map[string]string{"status": status}, nil)
}

// GetIssue fetches a single issue by ID.
func (c *Client) GetIssue(ctx context.Context, workspaceID, issueID string) (workflow.Issue, error) {
	var out workflow.Issue
	path := fmt.Sprintf(workflow.MulticaIssueEndpoint, workspaceID, issueID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// CreateIssueComment posts a comment on an issue.
func (c *Client) CreateIssueComment(ctx context.Context, workspaceID, issueID, content string) error {
	path := fmt.Sprintf(workflow.MulticaIssueCommentsEndpoint, workspaceID, issueID)
	return c.request(ctx, http.MethodPost, path, map[string]string{"content": content}, nil)
}

// ListIssueComments fetches comments on an issue.
func (c *Client) ListIssueComments(ctx context.Context, workspaceID, issueID string) ([]workflow.Comment, error) {
	var resp struct {
		Comments []workflow.Comment `json:"comments"`
	}
	path := fmt.Sprintf(workflow.MulticaIssueCommentsEndpoint, workspaceID, issueID)
	err := c.request(ctx, http.MethodGet, path, nil, &resp)
	return resp.Comments, err
}

// GetAttachment fetches attachment metadata (includes a signed download_url).
func (c *Client) GetAttachment(ctx context.Context, attachmentID string) (workflow.Attachment, error) {
	var out workflow.Attachment
	path := fmt.Sprintf(workflow.MulticaAttachmentEndpoint, attachmentID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// StartTask marks a task as started.
func (c *Client) StartTask(ctx context.Context, taskID string) error {
	path := fmt.Sprintf(workflow.MulticaTaskStartEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, nil, nil)
}

// CompleteTask marks a task as complete with the given output.
func (c *Client) CompleteTask(ctx context.Context, taskID string, output string) error {
	path := fmt.Sprintf(workflow.MulticaTaskCompleteEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"output": output}, nil)
}

// FailTask marks a task as failed with the given reason. failureReason is
// optional; pass "cancelled" for user-aborted tasks so multica does not
// auto-retry them.
func (c *Client) FailTask(ctx context.Context, taskID string, reason string, failureReason string) error {
	path := fmt.Sprintf(workflow.MulticaTaskFailEndpoint, taskID)
	body := map[string]any{"error": reason}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

// PostTaskMessages uploads task output as a single text message, matching
// multica's batch shape ({"messages": [{seq, type, content}]}).
func (c *Client) PostTaskMessages(ctx context.Context, taskID string, output string) error {
	path := fmt.Sprintf(workflow.MulticaTaskMessagesEndpoint, taskID)
	msgs := []workflow.TaskMessage{{Seq: 1, Type: "text", Content: output}}
	return c.request(ctx, http.MethodPost, path, map[string]any{"messages": msgs}, nil)
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

// CreateChatSession creates a new chat session for the given agent in the
// workspace. The returned ChatSession.ID is the row UUID used to bind tasks
// and node runs.
func (c *Client) CreateChatSession(ctx context.Context, workspaceID, agentID, title string) (workflow.ChatSession, error) {
	var out workflow.ChatSession
	path := "/api/chat/sessions"
	headers := http.Header{
		"X-Workspace-ID": []string{workspaceID},
	}
	err := c.doRequest(ctx, c.userBaseURL, http.MethodPost, path, headers, workflow.CreateChatSessionRequest{
		AgentID: agentID,
		Title:   title,
	}, &out)
	return out, err
}

// PinTaskSession persists the chat session binding for a task.
func (c *Client) PinTaskSession(ctx context.Context, taskID, sessionID, workDir string) error {
	path := fmt.Sprintf(workflow.MulticaTaskSessionEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, workflow.PinTaskSessionRequest{
		SessionID: sessionID,
		WorkDir:   workDir,
	}, nil)
}

// BindNodeRunSession persists the runtime/device/session binding for a
// workflow node run.
func (c *Client) BindNodeRunSession(ctx context.Context, nodeRunID, runtimeID, deviceID, sessionID string) error {
	path := fmt.Sprintf(workflow.MulticaNodeRunSessionEndpoint, nodeRunID)
	return c.request(ctx, http.MethodPost, path, workflow.BindNodeRunSessionRequest{
		RuntimeID: runtimeID,
		DeviceID:  deviceID,
		SessionID: sessionID,
	}, nil)
}
