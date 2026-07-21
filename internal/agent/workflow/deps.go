package workflow

import (
	"context"

	"cs-cloud/internal/provider"
)

// ConversationBinder creates a local conversation session for a workflow task
// so the frontend conversation proxy can resolve it by the multica chat
// session ID.
type ConversationBinder interface {
	Bind(ctx context.Context, sessionID, cwd string) error
}

// SessionRunner executes a workflow prompt inside an existing local
// conversation session and returns the final text output. When implemented,
// csc workflow tasks run in the bound session so the conversation page shows
// the live run and can be taken over afterwards.
type SessionRunner interface {
	RunSession(ctx context.Context, sessionID, cwd, prompt string) ([]byte, error)
}

// Dependencies holds the external dependencies required by the workflow driver.
type Dependencies struct {
	MulticaBaseURL string
	UserBaseURL    string
	TokenProvider  func() (*provider.Credentials, error)
	// DeviceID resolves the CoStrict Gateway device_id this daemon runs as.
	// It is used as the daemon_id when registering with multica. When nil,
	// daemon registration is disabled (the driver still runs tasks pushed
	// via localserver routes).
	DeviceID func() (string, error)
	// ConversationBinder creates a local csc session for the multica chat
	// session. When nil, session binding is skipped and the frontend
	// conversation proxy will not resolve the session.
	ConversationBinder ConversationBinder
	// SessionRunner runs the prompt in the bound local csc session. When nil,
	// csc tasks fall back to the one-shot CLI.
	SessionRunner SessionRunner
}
