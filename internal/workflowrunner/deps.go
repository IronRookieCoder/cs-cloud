package workflowrunner

import (
	"context"

	"cs-cloud/internal/provider"
)

// ConversationBinder creates a local conversation session for a workflow task
// so the frontend conversation proxy can resolve it by the server chat
// session ID. permMode selects the session permission mode (e.g.
// "bypassPermissions"); an empty string lets the backend pick its default.
type ConversationBinder interface {
	Bind(ctx context.Context, sessionID, cwd string, env []string, permMode string) error
}

// SessionRunner executes a workflow prompt inside an existing local
// conversation session and returns the final text output. When implemented,
// csc workflow tasks run in the bound session so the conversation page shows
// the live run and can be taken over afterwards. permMode is only used when
// the session has to be created.
type SessionRunner interface {
	RunSession(ctx context.Context, sessionID, cwd, prompt string, env []string, permMode string) ([]byte, error)
}

// SessionAborter stops a prompt running in an already-bound conversation
// session. Implementations should treat repeated aborts as best-effort and
// idempotent because both the runner and its supervisor can observe cancellation.
type SessionAborter interface {
	AbortSession(ctx context.Context, sessionID string) error
}

// Dependencies holds the external dependencies required by the workflow driver.
type Dependencies struct {
	BackendBaseURL string
	UserBaseURL    string
	TokenProvider  func() (*provider.Credentials, error)
	// AgentEnv is the environment configured for the managed agent process.
	// Workflow addon installation must use the same values because csc skill
	// and plugin commands resolve cloud/catalog endpoints from env too.
	AgentEnv map[string]string
	// DeviceID resolves the CoStrict Gateway device_id this daemon runs as.
	// It is used as the daemon_id when registering with the server. When nil,
	// daemon registration is disabled (the driver still runs tasks pushed
	// via localserver routes).
	DeviceID func() (string, error)
	// ConversationBinder creates a local csc session for the server chat
	// session. When nil, session binding is skipped and the frontend
	// conversation proxy will not resolve the session.
	ConversationBinder ConversationBinder
	// SessionRunner runs the prompt in the bound local csc session with the
	// task environment. When nil, csc tasks fall back to the one-shot CLI.
	SessionRunner SessionRunner
}
