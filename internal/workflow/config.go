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
	AgentTimeout       time.Duration `json:"agent_timeout"`
	MaxConcurrentTasks int           `json:"max_concurrent_tasks"`
}

func DefaultConfig() Config {
	appDir := platform.AppDir()
	return Config{
		MulticaBaseURL:     "https://api.multica.ai",
		WorkspacesRoot:     filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:           filepath.Join(appDir, "workflow", "cache"),
		SyncInterval:       5 * time.Minute,
		GCInterval:         24 * time.Hour,
		AgentTimeout:       30 * time.Minute,
		MaxConcurrentTasks: 20,
	}
}
