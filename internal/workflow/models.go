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

// RepoSpec mirrors the server's csCloudRepoSpec.
type RepoSpec struct {
	URL        string `json:"url"`
	Provider   string `json:"provider"`
	Role       string `json:"role"`
	BaseBranch string `json:"base_branch,omitempty"`
	Alias      string `json:"alias,omitempty"`
	BotToken   string `json:"bot_token,omitempty"`
}

// DeliverableSpec mirrors the server's csCloudDeliverableSpec.
type DeliverableSpec struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	RepoAlias string     `json:"repo_alias,omitempty"`
	Report    ReportSpec `json:"report"`
}

// ReportSpec mirrors the server's csCloudReportSpec.
type ReportSpec struct {
	Endpoint  string `json:"endpoint"`
	Method    string `json:"method"`
	BodyField string `json:"body_field"`
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
	// RepoURL is the workspace/project code repository the agent should clone
	// into its worktree and develop in. Empty for tasks without a code repo
	// (the worktree is then a scratch dir, as before).
	// deprecated: superseded by Repos; will be removed in M2 cleanup.
	RepoURL      string            `json:"repo_url,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	Repos        []RepoSpec        `json:"repos,omitempty"`
	Deliverables []DeliverableSpec `json:"deliverables,omitempty"`
	// PriorSessionID is the csc session id of the last task on the same
	// (agent, issue), letting this task resume the conversation. Empty on first
	// round / manual rerun / runtime mismatch. Mirrors the server's payload.
	PriorSessionID string `json:"prior_session_id,omitempty"`
	// PriorWorkDir is the workdir of the last task on the same (agent, issue),
	// so this task reuses (resets) the same checkout. Empty on first round.
	PriorWorkDir string `json:"prior_work_dir,omitempty"`
}

// CreateChatSessionRequest mirrors the server's POST
// /api/workspaces/{id}/api/chat/sessions body.
type CreateChatSessionRequest struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

// ChatSession mirrors the subset of the server's chat session response that
// cs-cloud needs to bind a workflow task/node run to a session.
type ChatSession struct {
	ID        string  `json:"id"`
	SessionID *string `json:"session_id,omitempty"`
	AgentID   string  `json:"agent_id"`
	Title     string  `json:"title"`
}

// PinTaskSessionRequest mirrors the server's POST
// /api/daemon/tasks/{taskId}/session body.
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

// BindNodeRunSessionRequest mirrors the server's POST
// /api/daemon/node-runs/{nodeRunId}/session body.
type BindNodeRunSessionRequest struct {
	RuntimeID string `json:"runtime_id,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// TaskMessage mirrors one entry of the server's POST
// /api/daemon/tasks/{id}/messages batch body.
type TaskMessage struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// DaemonRuntime describes one runtime reported during daemon registration.
// Type becomes the server provider field; it must be "cs-cloud" for the
// issue-conversation flow to discover this device.
type DaemonRuntime struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// DaemonRegisterRequest mirrors the server's POST /api/daemon/register body.
type DaemonRegisterRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	DaemonID    string          `json:"daemon_id"`
	DeviceName  string          `json:"device_name,omitempty"`
	CLIVersion  string          `json:"cli_version,omitempty"`
	Runtimes    []DaemonRuntime `json:"runtimes"`
}

// DaemonRuntimeResponse is one registered runtime row returned by the server.
// Only the fields cs-cloud needs are decoded.
type DaemonRuntimeResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
}

// DaemonRegisterResponse is the envelope returned by the server's POST
// /api/daemon/register. The runtimes array contains the registered rows.
type DaemonRegisterResponse struct {
	Runtimes     []DaemonRuntimeResponse `json:"runtimes"`
	Repos        []any                   `json:"repos,omitempty"`
	ReposVersion string                  `json:"repos_version,omitempty"`
	Settings     map[string]any          `json:"settings,omitempty"`
}
