package workflow

import (
	"path/filepath"
	"time"

	"cs-cloud/internal/platform"
)

type Config struct {
	MulticaBaseURL     string        `json:"multica_base_url"`
	WorkspacesRoot     string        `json:"workspaces_root"`
	CacheDir           string        `json:"cache_dir"`
	SyncInterval       time.Duration `json:"sync_interval"`
	GCInterval         time.Duration `json:"gc_interval"`
	HeartbeatInterval  time.Duration `json:"heartbeat_interval"`
	AgentTimeout       time.Duration `json:"agent_timeout"`
	MaxConcurrentTasks int           `json:"max_concurrent_tasks"`
	AllowedAgents      []string      `json:"allowed_agents"`
}

func DefaultConfig() Config {
	appDir := platform.AppDir()
	return Config{
		MulticaBaseURL:     "",
		WorkspacesRoot:     filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:           filepath.Join(appDir, "workflow", "cache"),
		SyncInterval:       5 * time.Minute,
		GCInterval:         24 * time.Hour,
		HeartbeatInterval:  15 * time.Second,
		AgentTimeout:       30 * time.Minute,
		MaxConcurrentTasks: 20,
		AllowedAgents: []string{
			"claude",
			"codex",
			"csc",
			"cs",
			"acp",
		},
	}
}
