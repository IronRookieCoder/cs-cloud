package workflow

const (
	DaemonRegisterEndpoint   = "/api/daemon/register"
	DaemonHeartbeatEndpoint  = "/api/daemon/heartbeat"
	DaemonDeregisterEndpoint = "/api/daemon/deregister"

	TaskClaimEndpoint      = "/api/daemon/runtimes/%s/tasks/claim"
	TaskStartEndpoint      = "/api/daemon/tasks/%s/start"
	TaskCompleteEndpoint   = "/api/daemon/tasks/%s/complete"
	TaskFailEndpoint       = "/api/daemon/tasks/%s/fail"
	TaskUsageEndpoint      = "/api/daemon/tasks/%s/usage"
	TaskMessagesEndpoint   = "/api/daemon/tasks/%s/messages"
	TaskSessionEndpoint    = "/api/daemon/tasks/%s/session"
	NodeRunSessionEndpoint = "/api/daemon/node-runs/%s/session"

	// gc-check endpoints (GET). cs-cloud's gcLoop calls these to decide whether
	// a task workdir is reclaimable; a 404 means the parent record is gone.
	IssueGCCheckEndpoint           = "/api/daemon/issues/%s/gc-check"
	ChatSessionGCCheckEndpoint     = "/api/daemon/chat-sessions/%s/gc-check"
	AutopilotRunGCCheckEndpoint    = "/api/daemon/autopilot-runs/%s/gc-check"
	TaskGCCheckEndpoint            = "/api/daemon/tasks/%s/gc-check"
	WorkflowNodeRunGCCheckEndpoint = "/api/daemon/workflow-node-runs/%s/gc-check"

	WorkspacesEndpoint = "/api/workspaces"
	IssuesEndpoint     = "/api/workspaces/%s/issues"
	IssueEndpoint      = "/api/workspaces/%s/issues/%s"
	ProjectsEndpoint   = "/api/workspaces/%s/projects"

	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
)
