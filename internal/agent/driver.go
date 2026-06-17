package agent

import (
	"context"
	"io"
)

type ProxyRoute struct {
	Method      string
	Prefix      string
	Rewrite     func(pathValues map[string]string) string
	Transform   func(io.ReadCloser) io.ReadCloser
}

type Driver interface {
	Name() string
	Detect(ctx context.Context) ([]DetectedAgent, error)
	CreateAgent(cfg AgentConfig) (Agent, error)
	HealthCheck(ctx context.Context, backend string) (*HealthResult, error)
	ProxyRoutes() []ProxyRoute
	HeaderMap() map[string]string
	FetchCommands(endpoint string) ([]SlashCommand, error)
	PrewarmPaths() []string
	Version() (string, error)
}

type DetectedAgent struct {
	Backend   string   `json:"backend"`
	Name      string   `json:"name"`
	Driver    string   `json:"driver"`
	CLIPath   string   `json:"cli_path,omitempty"`
	Available bool     `json:"available"`
	Extra     any      `json:"extra,omitempty"`
}

type HealthResult struct {
	Available bool   `json:"available"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Version   string   `json:"version,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type AgentConfig struct {
	ID          string            `json:"id"`
	Backend     string            `json:"backend"`
	DriverName  string            `json:"driver"`
	WorkingDir  string            `json:"working_dir"`
	Endpoint    string            `json:"endpoint,omitempty"`
	CustomEnv   map[string]string `json:"custom_env,omitempty"`
	Extra       map[string]any    `json:"extra,omitempty"`
}
