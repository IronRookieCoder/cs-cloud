package agent

import "errors"

// ErrEmptySessionOutput means an agent run reached a successful terminal state
// without producing an assistant response that a workflow can consume.
var ErrEmptySessionOutput = errors.New("session completed without assistant output")

// ErrIncomplete means a workflow task's agent session went idle (the agent
// stopped producing output) without ever invoking the explicit "complete task"
// tool. The driver maps this to the agent_incomplete failure reason so it can
// be distinguished from agent_timeout (the agent was still actively working
// when the deadline hit).
var ErrIncomplete = errors.New("agent stopped without calling the complete tool")
