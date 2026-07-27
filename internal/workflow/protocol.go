package workflow

const (
	MulticaDaemonRegisterEndpoint   = "/api/daemon/register"
	MulticaDaemonHeartbeatEndpoint  = "/api/daemon/heartbeat"
	MulticaDaemonDeregisterEndpoint = "/api/daemon/deregister"

	MulticaTaskClaimEndpoint    = "/api/daemon/runtimes/%s/tasks/claim"
	MulticaTaskStartEndpoint    = "/api/daemon/tasks/%s/start"
	MulticaTaskCompleteEndpoint = "/api/daemon/tasks/%s/complete"
	MulticaTaskFailEndpoint     = "/api/daemon/tasks/%s/fail"
	MulticaTaskUsageEndpoint    = "/api/daemon/tasks/%s/usage"
	MulticaTaskMessagesEndpoint = "/api/daemon/tasks/%s/messages"
	MulticaTaskSessionEndpoint    = "/api/daemon/tasks/%s/session"
	MulticaNodeRunSessionEndpoint = "/api/daemon/node-runs/%s/session"

	// gc-check endpoints (GET). cs-cloud's gcLoop calls these to decide whether
	// a task workdir is reclaimable; a 404 means the parent record is gone.
	MulticaIssueGCCheckEndpoint           = "/api/daemon/issues/%s/gc-check"
	MulticaChatSessionGCCheckEndpoint     = "/api/daemon/chat-sessions/%s/gc-check"
	MulticaAutopilotRunGCCheckEndpoint    = "/api/daemon/autopilot-runs/%s/gc-check"
	MulticaTaskGCCheckEndpoint            = "/api/daemon/tasks/%s/gc-check"
	MulticaWorkflowNodeRunGCCheckEndpoint = "/api/daemon/workflow-node-runs/%s/gc-check"

	MulticaWorkspacesEndpoint = "/api/workspaces"
	MulticaIssuesEndpoint     = "/api/workspaces/%s/issues"
	MulticaIssueEndpoint      = "/api/workspaces/%s/issues/%s"
	MulticaProjectsEndpoint   = "/api/workspaces/%s/projects"

	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
)
