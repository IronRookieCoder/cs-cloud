package localserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"cs-cloud/internal/agent/csc"
	"cs-cloud/internal/workflowrunner"
)

const sessionBindingCacheTTL = 30 * time.Second

// sessionAdopter recognises prompts addressed to sessions bound to failed
// workflow tasks and reopens the task so the user's turn drives the workflow.
// Every failure degrades to a plain proxied conversation — adopting is a
// best-effort overlay, never a blocker for sending messages.
type sessionAdopter struct {
	bindings bindingClient                              // GetSessionBinding/ResumeBeginTask narrow interface
	driver   turnAdopter                                // AdoptUserTurn narrow interface
	resolve  func(sessionID string) (*csc.Agent, error) // same agent the proxy forwards to
	mu       sync.Map                                   // sessionID -> cachedBinding
}

type bindingClient interface {
	GetSessionBinding(ctx context.Context, sessionID string) (*workflowrunner.SessionBinding, error)
	ResumeBeginTask(ctx context.Context, taskID, sessionID string) error
}

type turnAdopter interface {
	AdoptUserTurn(ctx context.Context, taskID, sessionID string, agent *csc.Agent) error
}

type cachedBinding struct {
	binding *workflowrunner.SessionBinding
	expires time.Time
}

// maybeAdopt is called from the proxy path before forwarding a prompt. It
// never reports errors upward; any failure leaves the original prompt untouched.
func (s *sessionAdopter) maybeAdopt(ctx context.Context, sessionID string) {
	binding, err := s.cachedBinding(ctx, sessionID)
	if err != nil || binding == nil || !binding.Resumable {
		return
	}

	if err := s.bindings.ResumeBeginTask(ctx, binding.TaskID, sessionID); err != nil {
		if !errors.Is(err, workflowrunner.ErrTaskNotResumable) {
			slog.Warn("session adopt: resume-begin failed", "session_id", sessionID, "error", err)
		}
		s.invalidate(sessionID)
		return
	}

	agent, err := s.resolve(sessionID)
	if err != nil {
		slog.Warn("session adopt: resolve agent failed", "session_id", sessionID, "error", err)
		return
	}

	if err := s.driver.AdoptUserTurn(ctx, binding.TaskID, sessionID, agent); err != nil && !errors.Is(err, workflowrunner.ErrTaskAlreadyRunning) {
		slog.Warn("session adopt: adopt failed", "session_id", sessionID, "error", err)
	}
}

// cachedBinding returns the cached workflow binding for a session, fetching
// and caching it on miss. Negative results (no binding or resumable=false) are
// cached with the same TTL so ordinary conversation prompts do not hit the
// server on every request.
func (s *sessionAdopter) cachedBinding(ctx context.Context, sessionID string) (*workflowrunner.SessionBinding, error) {
	if v, ok := s.mu.Load(sessionID); ok {
		cb := v.(cachedBinding)
		if time.Now().Before(cb.expires) {
			return cb.binding, nil
		}
		s.mu.Delete(sessionID)
	}

	binding, err := s.bindings.GetSessionBinding(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, workflowrunner.ErrSessionNotBound) {
			return nil, err
		}
		s.mu.Store(sessionID, cachedBinding{binding: nil, expires: time.Now().Add(sessionBindingCacheTTL)})
		return nil, nil
	}

	s.mu.Store(sessionID, cachedBinding{binding: binding, expires: time.Now().Add(sessionBindingCacheTTL)})
	return binding, nil
}

func (s *sessionAdopter) invalidate(sessionID string) {
	s.mu.Delete(sessionID)
}
