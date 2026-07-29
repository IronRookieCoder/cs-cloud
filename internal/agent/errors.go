package agent

import "errors"

// ErrEmptySessionOutput means an agent run reached a successful terminal state
// without producing an assistant response that a workflow can consume.
var ErrEmptySessionOutput = errors.New("session completed without assistant output")
