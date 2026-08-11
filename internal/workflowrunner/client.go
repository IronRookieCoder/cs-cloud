package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

// Client is a REST client for the backend.
type Client struct {
	baseURL       string
	userBaseURL   string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

// StatusError carries the HTTP status of a failed server call so callers
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

// ErrRuntimeGone is returned (wrapped) when the server reports the runtime row
// no longer exists; the driver should re-register.
var ErrRuntimeGone = errors.New("runtime gone")

// NewClient creates a new server REST client.
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
		logger.Warn("workflow: server %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
		return &StatusError{Method: method, URL: url, Path: path, StatusCode: resp.StatusCode, Body: string(b)}
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GetWorkspaces fetches all workspaces from the server.
func (c *Client) GetWorkspaces(ctx context.Context) ([]workflow.Workspace, error) {
	var out []workflow.Workspace
	err := c.request(ctx, http.MethodGet, workflow.WorkspacesEndpoint, nil, &out)
	return out, err
}

// GetProjects fetches all projects for a workspace from the server.
func (c *Client) GetProjects(ctx context.Context, workspaceID string) ([]workflow.Project, error) {
	var out []workflow.Project
	path := fmt.Sprintf(workflow.ProjectsEndpoint, workspaceID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// ListIssues fetches issues for a workspace.
func (c *Client) ListIssues(ctx context.Context, workspaceID string) ([]workflow.Issue, error) {
	var out []workflow.Issue
	path := fmt.Sprintf(workflow.IssuesEndpoint, workspaceID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// CreateIssue creates a new issue in the workspace.
func (c *Client) CreateIssue(ctx context.Context, workspaceID, title, description string) (workflow.Issue, error) {
	var out workflow.Issue
	path := fmt.Sprintf(workflow.IssuesEndpoint, workspaceID)
	err := c.request(ctx, http.MethodPost, path, map[string]string{
		"title":       title,
		"description": description,
	}, &out)
	return out, err
}

// UpdateIssueStatus updates an issue's status.
func (c *Client) UpdateIssueStatus(ctx context.Context, workspaceID, issueID, status string) error {
	path := fmt.Sprintf(workflow.IssueEndpoint, workspaceID, issueID)
	return c.request(ctx, http.MethodPut, path, map[string]string{"status": status}, nil)
}

// GetIssue fetches a single issue by ID.
func (c *Client) GetIssue(ctx context.Context, workspaceID, issueID string) (workflow.Issue, error) {
	var out workflow.Issue
	path := fmt.Sprintf(workflow.IssueEndpoint, workspaceID, issueID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// StartTask marks a task as started.
func (c *Client) StartTask(ctx context.Context, taskID string) error {
	path := fmt.Sprintf(workflow.TaskStartEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, nil, nil)
}

// CompleteTask marks a task as complete with the given output. sessionID and
// workDir, when non-empty, are forwarded so the server preserves the resume
// pointer — its CompleteAgentTask SQL is a direct SET (not COALESCE), so
// omitting them NULLs the columns that bindSession pinned mid-run and breaks
// GetLastTaskSession for the next round.
func (c *Client) CompleteTask(ctx context.Context, taskID, output, sessionID, workDir string, sig agent.CompletionSignal) error {
	path := fmt.Sprintf(workflow.TaskCompleteEndpoint, taskID)
	body := map[string]any{"output": output}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	// Forward the agent's explicit completion payload so multica can use the
	// critic's decision directly (review) instead of parsing session text.
	if sig.Decision != "" {
		body["decision"] = sig.Decision
	}
	if sig.Reason != "" {
		body["reason"] = sig.Reason
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

// FailTask marks a task as failed with the given reason. failureReason is
// optional; pass "cancelled" for user-aborted tasks so the server does not
// auto-retry them.
func (c *Client) FailTask(ctx context.Context, taskID string, reason string, failureReason string) error {
	path := fmt.Sprintf(workflow.TaskFailEndpoint, taskID)
	body := map[string]any{"error": reason}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

// PostTaskFact delivers a task outcome to the modern /facts endpoint. If the
// server responds 404 (old server without the endpoint), it falls back to the
// legacy /complete or /fail path based on the fact kind. The returned error is
// the delivery result: nil means accepted, *StatusError exposes HTTP status for
// caller-side retry/ignore decisions.
func (c *Client) PostTaskFact(ctx context.Context, f OutboxFact) error {
	path := fmt.Sprintf(workflow.TaskFactsEndpoint, url.PathEscape(f.TaskID))
	err := c.request(ctx, http.MethodPost, path, f, nil)
	if err == nil {
		return nil
	}
	var stErr *StatusError
	if !errors.As(err, &stErr) || stErr.StatusCode != http.StatusNotFound {
		return err
	}
	// Old server: fall back to legacy endpoint with the same payload shape.
	if f.Kind == "complete" {
		return c.CompleteTask(ctx, f.TaskID, f.Output, f.SessionID, f.WorkDir, agent.CompletionSignal{
			Decision: f.Decision,
			Reason:   f.Reason,
		})
	}
	return c.FailTask(ctx, f.TaskID, f.Error, f.FailureReason)
}

// PostTaskMessages uploads task output as a single text message, matching
// the server's batch shape ({"messages": [{seq, type, content}]}).
func (c *Client) PostTaskMessages(ctx context.Context, taskID string, output string) error {
	path := fmt.Sprintf(workflow.TaskMessagesEndpoint, taskID)
	msgs := []workflow.TaskMessage{{Seq: 1, Type: "text", Content: output}}
	return c.request(ctx, http.MethodPost, path, map[string]any{"messages": msgs}, nil)
}

// RegisterDaemon registers this device as a cs-cloud runtime in the given
// workspace and returns the registered runtime rows (with their IDs).
func (c *Client) RegisterDaemon(ctx context.Context, req workflow.DaemonRegisterRequest) ([]workflow.DaemonRuntimeResponse, error) {
	var out workflow.DaemonRegisterResponse
	err := c.request(ctx, http.MethodPost, workflow.DaemonRegisterEndpoint, req, &out)
	return out.Runtimes, err
}

// Heartbeat keeps a registered runtime alive. It returns ErrRuntimeGone
// (wrapped) when the server responds 404, meaning the row was deleted and the
// caller should re-register.
func (c *Client) Heartbeat(ctx context.Context, runtimeID string) error {
	err := c.request(ctx, http.MethodPost, workflow.DaemonHeartbeatEndpoint, map[string]any{"runtime_id": runtimeID}, nil)
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
	return c.request(ctx, http.MethodPost, workflow.DaemonDeregisterEndpoint, map[string]any{"runtime_ids": runtimeIDs}, nil)
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
	path := fmt.Sprintf(workflow.TaskSessionEndpoint, url.PathEscape(taskID))
	return c.request(ctx, http.MethodPost, path, workflow.PinTaskSessionRequest{
		SessionID: sessionID,
		WorkDir:   workDir,
	}, nil)
}

// BindNodeRunSession persists the runtime/device/session binding for a
// workflow node run.
func (c *Client) BindNodeRunSession(ctx context.Context, nodeRunID, runtimeID, deviceID, sessionID string) error {
	path := fmt.Sprintf(workflow.NodeRunSessionEndpoint, url.PathEscape(nodeRunID))
	return c.request(ctx, http.MethodPost, path, workflow.BindNodeRunSessionRequest{
		RuntimeID: runtimeID,
		DeviceID:  deviceID,
		SessionID: sessionID,
	}, nil)
}

// GCCheckStatus is the minimal payload returned by the server's gc-check endpoints.
// UpdatedAt is the TTL anchor for issue/chat rows; CompletedAt for
// autopilot/task/node-run. Either may be zero on non-terminal rows. A 404 is
// returned as *StatusError (StatusCode == 404) so callers can distinguish
// "parent record gone" from transient errors.
type GCCheckStatus struct {
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// GetIssueGCCheck queries issue status + updated_at for the GC loop.
func (c *Client) GetIssueGCCheck(ctx context.Context, issueID string) (GCCheckStatus, error) {
	var out GCCheckStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf(workflow.IssueGCCheckEndpoint, issueID), nil, &out)
	return out, err
}

// GetChatSessionGCCheck queries chat-session status + updated_at. A 404 means
// the session was hard-deleted — the strongest reclaim signal.
func (c *Client) GetChatSessionGCCheck(ctx context.Context, sessionID string) (GCCheckStatus, error) {
	var out GCCheckStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf(workflow.ChatSessionGCCheckEndpoint, sessionID), nil, &out)
	return out, err
}

// GetAutopilotRunGCCheck queries autopilot-run status + completed_at.
func (c *Client) GetAutopilotRunGCCheck(ctx context.Context, runID string) (GCCheckStatus, error) {
	var out GCCheckStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf(workflow.AutopilotRunGCCheckEndpoint, runID), nil, &out)
	return out, err
}

// GetTaskGCCheck queries agent-task status + completed_at (quick-create path).
func (c *Client) GetTaskGCCheck(ctx context.Context, taskID string) (GCCheckStatus, error) {
	var out GCCheckStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf(workflow.TaskGCCheckEndpoint, taskID), nil, &out)
	return out, err
}

// GetWorkflowNodeRunGCCheck queries workflow-node-run status + completed_at.
// cs-cloud's workflow tasks (issue_id NULL) key their workdir GC on the node run.
func (c *Client) GetWorkflowNodeRunGCCheck(ctx context.Context, nodeRunID string) (GCCheckStatus, error) {
	var out GCCheckStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf(workflow.WorkflowNodeRunGCCheckEndpoint, nodeRunID), nil, &out)
	return out, err
}

// SessionBinding resolves which workflow task owns a CSC session.
type SessionBinding struct {
	TaskID        string `json:"task_id"`
	TaskStatus    string `json:"task_status"`
	NodeRunID     string `json:"node_run_id"`
	NodeRunStatus string `json:"node_run_status"`
	Resumable     bool   `json:"resumable"`
}

// ErrSessionNotBound is returned by GetSessionBinding when the server reports
// the session has no associated workflow binding.
var ErrSessionNotBound = errors.New("session has no workflow binding")

// ErrTaskNotResumable is returned by ResumeBeginTask when the server reports
// the task cannot be resumed.
var ErrTaskNotResumable = errors.New("task not resumable")

// GetSessionBinding resolves which workflow task (if any) owns a CSC session.
// A 404 response maps to ErrSessionNotBound so the proxy layer can fall
// through to a plain conversation.
func (c *Client) GetSessionBinding(ctx context.Context, sessionID string) (*SessionBinding, error) {
	path := fmt.Sprintf("/api/daemon/sessions/%s/binding", url.PathEscape(sessionID))
	var binding SessionBinding
	err := c.request(ctx, http.MethodGet, path, nil, &binding)
	if err != nil {
		var stErr *StatusError
		if errors.As(err, &stErr) && stErr.StatusCode == http.StatusNotFound {
			return nil, ErrSessionNotBound
		}
		return nil, err
	}
	return &binding, nil
}

// ResumeBeginTask asks multica to reopen a failed task for a user-driven turn.
// A 409 response maps to ErrTaskNotResumable; callers treat it as a plain
// conversation.
func (c *Client) ResumeBeginTask(ctx context.Context, taskID, sessionID string) error {
	path := fmt.Sprintf("/api/daemon/tasks/%s/resume-begin", url.PathEscape(taskID))
	err := c.request(ctx, http.MethodPost, path, map[string]string{"session_id": sessionID}, nil)
	if err != nil {
		var stErr *StatusError
		if errors.As(err, &stErr) && stErr.StatusCode == http.StatusConflict {
			return ErrTaskNotResumable
		}
		return err
	}
	return nil
}
