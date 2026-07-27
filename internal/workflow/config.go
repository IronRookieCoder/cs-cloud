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

	// GC reclaims task workdirs whose parent record is terminal+stale. Defaults
	// mirror multica's daemon so cs-cloud reclaims at the same cadence. All
	// overridable via CS_CLOUD_WORKFLOW_GC_* env.
	GCEnabled          bool          `json:"gc_enabled"`
	GCTTL              time.Duration `json:"gc_ttl"`          // terminal issue/node-run → clean whole dir after this
	GCOrphanTTL        time.Duration `json:"gc_orphan_ttl"`   // no/unknown meta → clean by mtime after this
	GCArtifactTTL      time.Duration `json:"gc_artifact_ttl"` // open issue → drop only regenerable artifacts after this
	GCArtifactPatterns []string      `json:"gc_artifact_patterns"`
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
		GCEnabled:          true,
		GCTTL:              24 * time.Hour,
		GCOrphanTTL:        72 * time.Hour,
		GCArtifactTTL:      12 * time.Hour,
		GCArtifactPatterns: []string{"node_modules", ".next", ".turbo"},
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
