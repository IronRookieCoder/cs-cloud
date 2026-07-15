package workflow

import "time"

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
	return Config{
		MulticaBaseURL:     "https://api.multica.ai",
		WorkspacesRoot:     "${HOME}/.costrict/cs-cloud/workflow/workspaces",
		CacheDir:           "${HOME}/.costrict/cs-cloud/workflow/cache",
		SyncInterval:       5 * time.Minute,
		GCInterval:         24 * time.Hour,
		AgentTimeout:       30 * time.Minute,
		MaxConcurrentTasks: 20,
	}
}
