// Package sessionevent contains the logic that watches a stream of agent
// session events (busy/idle/result/error) and decides when a prompt turn has
// finished.
//
// It deliberately lives outside the concrete agent implementations: the
// workflow layer (adopted user turns, fed from the runtime EventBus) and the
// agent layer (dispatched runs, fed from a direct session SSE subscription)
// share a single, non-duplicated definition of "the session is done". The
// concrete agents stay responsible only for translation — opening the SSE
// stream and parsing it into agent.Event — not for this turn-completion
// business logic.
package sessionevent

import (
	"context"
	"fmt"

	"cs-cloud/internal/agent"
)

// WaitForSessionDone consumes session events until the prompt finishes. It
// gates completion on having seen the session go busy first, so an idle event
// emitted before our prompt starts cannot end the wait early.
func WaitForSessionDone(ctx context.Context, events <-chan agent.Event) error {
	busy := false
	awaitingContinuation := false
	terminalFailure := false
	terminalSubtype := ""
	terminalMessage := ""
	finishIdle := func() (bool, error) {
		if !busy {
			return false, nil
		}
		if awaitingContinuation && !terminalFailure {
			// The backend emits an idle boundary after a model turn that ended
			// in a tool call (or token-limit continuation). The same prompt
			// becomes busy again once tool execution/continuation resumes.
			busy = false
			return false, nil
		}
		return true, completionError(terminalFailure, terminalSubtype, terminalMessage)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("event stream closed before the session finished")
			}
			if ev.Type == agent.EventStreamClosed {
				// A bus-fed source (EmitSessionEvents) signals transport-close
				// this way because its delivery channel cannot close. Treat it
				// identically to a directly-closing channel.
				return fmt.Errorf("event stream closed before the session finished")
			}
			data, _ := ev.Data.(map[string]any)
			switch ev.Type {
			case "session.status":
				if status, ok := data["status"].(map[string]any); ok {
					if t, _ := status["type"].(string); t == "busy" {
						if !busy {
							busy = true
							awaitingContinuation = false
							terminalFailure = false
							terminalSubtype = ""
							terminalMessage = ""
						}
					} else if t == "idle" && busy {
						if done, err := finishIdle(); done {
							return err
						}
					}
				}
			case "session.result":
				if !busy {
					continue
				}
				terminalSubtype, _ = data["subtype"].(string)
				isError, _ := data["isError"].(bool)
				if snakeCaseError, _ := data["is_error"].(bool); snakeCaseError {
					isError = true
				}
				terminalFailure = isError || (terminalSubtype != "" && terminalSubtype != "success")
				stopReason, _ := data["stopReason"].(string)
				if stopReason == "" {
					stopReason, _ = data["stop_reason"].(string)
				}
				awaitingContinuation = stopReason == "tool_use" || stopReason == "max_tokens"
				if message := resultErrorMessage(data); message != "" {
					terminalMessage = message
				}
			case "session.error":
				if !busy {
					continue
				}
				errorData, _ := data["error"].(map[string]any)
				if subtype, _ := errorData["subtype"].(string); subtype != "api_retry" {
					if message, _ := errorData["message"].(string); message != "" {
						terminalMessage = message
					}
				}
			case "session.idle":
				if done, err := finishIdle(); done {
					return err
				}
			}
		}
	}
}

func resultErrorMessage(data map[string]any) string {
	if errorsList, ok := data["errors"].([]any); ok {
		for _, item := range errorsList {
			if message, ok := item.(string); ok && message != "" {
				return message
			}
			if errorData, ok := item.(map[string]any); ok {
				if message, _ := errorData["message"].(string); message != "" {
					return message
				}
			}
		}
	}
	if errorData, ok := data["error"].(map[string]any); ok {
		if message, _ := errorData["message"].(string); message != "" {
			return message
		}
	}
	if message, _ := data["error"].(string); message != "" {
		return message
	}
	if message, _ := data["message"].(string); message != "" {
		return message
	}
	return ""
}

func completionError(failed bool, subtype, message string) error {
	if !failed {
		return nil
	}
	if message != "" {
		return fmt.Errorf("%s", message)
	}
	if subtype != "" {
		return fmt.Errorf("csc session failed: %s", subtype)
	}
	return fmt.Errorf("csc session failed")
}
