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
	Agent       string            `json:"agent"`
	Prompt      string            `json:"prompt"`
	Env         map[string]string `json:"env,omitempty"`
	// Kind is the multica task kind (direct|comment|chat|quick_create|
	// autopilot). Optional; consumers may ignore it.
	Kind string `json:"kind,omitempty"`
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
