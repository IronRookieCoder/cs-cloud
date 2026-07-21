package workflow

import "time"

type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Issue struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Project struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	RepoURL     string `json:"repo_url,omitempty"`
}

type TaskStatus int

const (
	TaskStatusPending TaskStatus = iota
	TaskStatusRunning
	TaskStatusComplete
	TaskStatusFailed
	TaskStatusAborted
)

func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "pending"
	case TaskStatusRunning:
		return "running"
	case TaskStatusComplete:
		return "complete"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusAborted:
		return "aborted"
	default:
		return "unknown"
	}
}

type Task struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	IssueID     string     `json:"issue_id,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	Agent       string     `json:"agent"`
	Prompt      string     `json:"prompt"`
	Status      TaskStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
}

type TaskRunPayload struct {
	TaskID      string            `json:"task_id"`
	WorkspaceID string            `json:"workspace_id"`
	IssueID     string            `json:"issue_id,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	NodeRunID   string            `json:"node_run_id,omitempty"`
	AgentID     string            `json:"agent_id,omitempty"`
	Agent       string            `json:"agent"`
	Prompt      string            `json:"prompt"`
	Env         map[string]string `json:"env,omitempty"`
	// Kind is the multica task kind (direct|comment|chat|quick_create|
	// autopilot). Optional; consumers may ignore it.
	Kind string `json:"kind,omitempty"`
}

// CreateChatSessionRequest mirrors multica's POST
// /api/workspaces/{id}/api/chat/sessions body.
type CreateChatSessionRequest struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

// ChatSession mirrors the subset of multica's chat session response that
// cs-cloud needs to bind a workflow task/node run to a session.
type ChatSession struct {
	ID        string  `json:"id"`
	SessionID *string `json:"session_id,omitempty"`
	AgentID   string  `json:"agent_id"`
	Title     string  `json:"title"`
}

// PinTaskSessionRequest mirrors multica's POST
// /api/daemon/tasks/{taskId}/session body.
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

// BindNodeRunSessionRequest mirrors multica's POST
// /api/daemon/node-runs/{nodeRunId}/session body.
type BindNodeRunSessionRequest struct {
	RuntimeID string `json:"runtime_id,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// TaskMessage mirrors one entry of multica's POST
// /api/daemon/tasks/{id}/messages batch body.
type TaskMessage struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// DaemonRuntime describes one runtime reported during daemon registration.
// Type becomes the multica provider field; it must be "cs-cloud" for the
// issue-conversation flow to discover this device.
type DaemonRuntime struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// DaemonRegisterRequest mirrors multica's POST /api/daemon/register body.
type DaemonRegisterRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	DaemonID    string          `json:"daemon_id"`
	DeviceName  string          `json:"device_name,omitempty"`
	CLIVersion  string          `json:"cli_version,omitempty"`
	Runtimes    []DaemonRuntime `json:"runtimes"`
}

// DaemonRuntimeResponse is one registered runtime row returned by multica.
// Only the fields cs-cloud needs are decoded.
type DaemonRuntimeResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
}

// DaemonRegisterResponse is the envelope returned by multica's POST
// /api/daemon/register. The runtimes array contains the registered rows.
type DaemonRegisterResponse struct {
	Runtimes     []DaemonRuntimeResponse `json:"runtimes"`
	Repos        []any                   `json:"repos,omitempty"`
	ReposVersion string                  `json:"repos_version,omitempty"`
	Settings     map[string]any          `json:"settings,omitempty"`
}

type Comment struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	AuthorName string    `json:"author_name,omitempty"`
	AuthorType string    `json:"author_type,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}
