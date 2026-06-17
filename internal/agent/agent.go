package agent

import "context"

// ParseVersion extracts the version number from a version command output line.
// The output format is typically: "<version> (commit: ..., built: ...)"
// It returns the part before the first space, e.g. "4.2.3" or "4.2.3-beta".
func ParseVersion(raw string) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] == ' ' || raw[i] == '\t' {
			return raw[:i]
		}
	}
	return raw
}

type Agent interface {
	ID() string
	Backend() string
	Driver() string
	State() AgentState
	PID() int

	Start(ctx context.Context) error
	Kill() error

	SendMessage(ctx context.Context, msg PromptMessage) error
	CancelPrompt(ctx context.Context) error

	ConfirmPermission(ctx context.Context, callID string, optionID string) error
	PendingPermissions() []PermissionInfo

	GetModelInfo() *ModelInfo
	SetModel(ctx context.Context, modelID string) (*ModelInfo, error)

	SessionID() string
	SetEventEmitter(emitter func(Event))
}

type AgentState int

const (
	StateIdle AgentState = iota
	StateConnecting
	StateConnected
	StateSessionActive
	StateDisconnected
	StateError
)

func (s AgentState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateSessionActive:
		return "session_active"
	case StateDisconnected:
		return "disconnected"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

type PromptMessage struct {
	Content string   `json:"content"`
	Files   []string `json:"files,omitempty"`
}

type PermissionInfo struct {
	CallID  string             `json:"call_id"`
	Title   string             `json:"title"`
	Kind    string             `json:"kind"`
	Options []PermissionOption `json:"options"`
}

type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type ModelInfo struct {
	CurrentModelID    string       `json:"current_model_id"`
	CurrentModelLabel string       `json:"current_model_label"`
	AvailableModels   []ModelEntry `json:"available_models"`
	CanSwitch         bool         `json:"can_switch"`
}

type ModelEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Event struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"msg_id,omitempty"`
	Backend        string `json:"backend,omitempty"`
	Data           any    `json:"data"`
}
