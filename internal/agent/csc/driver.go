package csc

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"

	"cs-cloud/internal/agent"
)

type Driver struct {
	cmd agent.Command
}

func NewDriver(cmd agent.Command) *Driver {
	return &Driver{cmd: cmd}
}

func (d *Driver) Name() string { return "csc" }

func (d *Driver) Detect(ctx context.Context) ([]agent.DetectedAgent, error) {
	if d.cmd.IsZero() {
		return nil, nil
	}
	p, err := exec.LookPath(d.cmd.Binary())
	if err != nil {
		return nil, nil
	}
	_ = p
	return []agent.DetectedAgent{
		{
			Backend:   "csc",
			Name:      "CSC",
			Driver:    "http",
			Available: true,
			Extra: map[string]any{
				"command": d.cmd,
			},
		},
	}, nil
}

func (d *Driver) CreateAgent(cfg agent.AgentConfig) (agent.Agent, error) {
	cmd := d.cmd
	if extra := cfg.Extra; extra != nil {
		if c, ok := extra["command"].(agent.Command); ok && !c.IsZero() {
			cmd = c
		}
	}
	a := NewAgent(agent.AgentConfig{
		ID:         cfg.ID,
		Backend:    "csc",
		DriverName: "http",
		WorkingDir: cfg.WorkingDir,
		CustomEnv:  cfg.CustomEnv,
		Extra: map[string]any{
			"command": cmd,
		},
	})
	return a, nil
}

func (d *Driver) HealthCheck(ctx context.Context, backend string) (*agent.HealthResult, error) {
	return nil, fmt.Errorf("health check not supported in spawn mode")
}

func (d *Driver) HeaderMap() map[string]string {
	return map[string]string{
		"X-Workspace-Directory": "x-csc-directory",
	}
}

func (d *Driver) PrewarmPaths() []string {
	return []string{
		"/agent",
		"/command",
		"/provider/capabilities",
	}
}

func (d *Driver) ProxyRoutes() []agent.ProxyRoute {
	return []agent.ProxyRoute{
		{Method: http.MethodPost, Prefix: "/conversations", Rewrite: agent.RewriteTo("/session")},
		{Method: http.MethodGet, Prefix: "/conversations", Rewrite: agent.RewriteTo("/session")},
		{Method: http.MethodGet, Prefix: "/conversations/status", Rewrite: agent.RewriteTo("/session/status")},
		{Method: http.MethodGet, Prefix: "/conversations/{id}", Rewrite: agent.RewriteSessionID("/session/")},
		{Method: http.MethodPatch, Prefix: "/conversations/{id}", Rewrite: agent.RewriteSessionID("/session/")},
		{Method: http.MethodDelete, Prefix: "/conversations/{id}", Rewrite: agent.RewriteSessionID("/session/")},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/prompt", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/prompt_async"), Transform: agent.TransformPromptBody},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/prompt/async", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/prompt_async"), Transform: agent.TransformPromptBody},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/abort", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/abort")},
		{Method: http.MethodGet, Prefix: "/conversations/{id}/messages", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/message")},
		{Method: http.MethodGet, Prefix: "/conversations/{id}/todo", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/todo")},
		{Method: http.MethodGet, Prefix: "/conversations/{id}/tasks", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/tasks")},
		{Method: http.MethodGet, Prefix: "/conversations/{id}/diff", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/diff")},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/shell", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/shell")},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/command", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/command")},
		{Method: http.MethodPost, Prefix: "/conversations/{id}/command/async", Rewrite: agent.RewriteSessionIDWithSuffix("/session/", "/command_async")},
		{Method: http.MethodGet, Prefix: "/permissions", Rewrite: agent.RewriteTo("/permission")},
		{Method: http.MethodPost, Prefix: "/permissions/{id}/reply", Rewrite: agent.RewritePermReply, Transform: agent.RenameJSONFieldAny([]string{"decision", "reply"}, "behavior")},
		{Method: http.MethodGet, Prefix: "/questions", Rewrite: agent.RewriteTo("/question")},
		{Method: http.MethodPost, Prefix: "/questions/{id}/reply", Rewrite: agent.RewriteQuestionAction("/reply")},
		{Method: http.MethodPost, Prefix: "/questions/{id}/reject", Rewrite: agent.RewriteQuestionAction("/reject")},
		{Method: http.MethodGet, Prefix: "/events", Rewrite: agent.RewriteTo("/event")},
		{Method: http.MethodGet, Prefix: "/agents/favorites", Rewrite: agent.RewriteTo("/global/favorite/skills")},
		{Method: http.MethodPost, Prefix: "/agents/favorites/{id}/load", Rewrite: agent.RewriteSessionIDWithSuffix("/global/favorite/skills/", "/load")},
		{Method: http.MethodPost, Prefix: "/agents/favorites/{id}/unload", Rewrite: agent.RewriteSessionIDWithSuffix("/global/favorite/skills/", "/unload")},
		{Method: http.MethodGet, Prefix: "/agents/models", Rewrite: agent.RewriteTo("/provider/capabilities")},
		{Method: http.MethodGet, Prefix: "/agents/session-modes", Rewrite: agent.RewriteTo("/agent")},
		// /agents/commands is now handled by localserver.handleCommands (merged manifest with scope filtering)
		// {Method: http.MethodGet, Prefix: "/agents/commands", Rewrite: agent.RewriteTo("/command")},
		{Method: http.MethodGet, Prefix: "/agents/mcp", Rewrite: agent.RewriteTo("/mcp")},
		{Method: http.MethodGet, Prefix: "/agents/lsp", Rewrite: agent.RewriteTo("/lsp")},
	}
}
