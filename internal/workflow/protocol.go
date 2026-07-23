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
	MulticaTaskSessionEndpoint  = "/api/daemon/tasks/%s/session"
	MulticaNodeRunSessionEndpoint = "/api/daemon/node-runs/%s/session"

	MulticaWorkspacesEndpoint  = "/api/workspaces"
	MulticaIssuesEndpoint      = "/api/workspaces/%s/issues"
	MulticaIssueEndpoint       = "/api/workspaces/%s/issues/%s"
	MulticaIssueCommentsEndpoint = "/api/workspaces/%s/issues/%s/comments"
	MulticaProjectsEndpoint    = "/api/workspaces/%s/projects"

	MulticaAttachmentEndpoint  = "/api/attachments/%s"

	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
)
