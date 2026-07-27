package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/workflow"
)

func Load() (*Config, error) {
	cfg := &Config{
		CloudBaseURL:        platform.Getenv("CLOUD_BASE_URL"),
		BaseURL:             platform.Getenv("COSTRICT_BASE_URL"),
		DefaultShell:        platform.Getenv("CS_CLOUD_SHELL"),
		DefaultAgent:        platform.Getenv("CS_CLOUD_DEFAULT_AGENT"),
		AgentPath:           platform.Getenv("CS_CLOUD_AGENT_PATH"),
		AgentCommand:        platform.Getenv("CS_CLOUD_AGENT_COMMAND"),
		AgentVersionCommand: platform.Getenv("CS_CLOUD_AGENT_VERSION_COMMAND"),
		Workflow:            workflow.DefaultConfig(),
	}

	if cfg.CloudBaseURL == "" {
		cfg.CloudBaseURL = platform.Getenv("COSTRICT_CLOUD_BASE_URL")
	}

	// Workflow config from environment variables.
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_MULTICA_BASE_URL"); v != "" {
		cfg.Workflow.MulticaBaseURL = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_WORKSPACES_ROOT"); v != "" {
		cfg.Workflow.WorkspacesRoot = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_CACHE_DIR"); v != "" {
		cfg.Workflow.CacheDir = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_SYNC_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.SyncInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_ENABLED"); v != "" {
		cfg.Workflow.GCEnabled = v == "true" || v == "1" || v == "yes"
	} else if platform.Getenv("CS_CLOUD_WORKFLOW_GC_DISABLED") != "" {
		// Explicit opt-out for operators who want to keep every workdir.
		cfg.Workflow.GCEnabled = false
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCTTL = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_ORPHAN_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCOrphanTTL = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_ARTIFACT_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCArtifactTTL = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_ARTIFACT_PATTERNS"); v != "" {
		cfg.Workflow.GCArtifactPatterns = strings.Split(v, ",")
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_HEARTBEAT_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.HeartbeatInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_AGENT_TIMEOUT"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.AgentTimeout = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workflow.MaxConcurrentTasks = n
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_ALLOWED_AGENTS"); v != "" {
		cfg.Workflow.AllowedAgents = strings.Split(v, ",")
	}

	if envJSON := platform.Getenv("CS_CLOUD_AGENT_ENV"); envJSON != "" {
		var env map[string]string
		if err := json.Unmarshal([]byte(envJSON), &env); err == nil {
			cfg.AgentEnv = env
		}
	}

	if env := platform.Getenv("CS_CLOUD_AUTO_UPGRADE"); env != "" {
		cfg.AutoUpgrade = env == "true" || env == "1" || env == "yes"
	}
	if platform.NoAutoUpgrade() {
		cfg.AutoUpgrade = false
	}

	if p, err := configFilePath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			var fileCfg Config
			if err := json.Unmarshal(b, &fileCfg); err == nil {
				if cfg.CloudBaseURL == "" {
					cfg.CloudBaseURL = fileCfg.CloudBaseURL
				}
				if cfg.BaseURL == "" {
					cfg.BaseURL = fileCfg.BaseURL
				}
				if cfg.DefaultShell == "" {
					cfg.DefaultShell = fileCfg.DefaultShell
				}
				if cfg.AgentCommand == "" {
					cfg.AgentCommand = fileCfg.AgentCommand
				}
				if cfg.AgentPath == "" {
					cfg.AgentPath = fileCfg.AgentPath
				}
				if cfg.DefaultAgent == "" {
					cfg.DefaultAgent = fileCfg.DefaultAgent
				}
				if cfg.AgentEnv == nil && fileCfg.AgentEnv != nil {
					cfg.AgentEnv = fileCfg.AgentEnv
				}
				if cfg.AgentWorkspace == "" {
					cfg.AgentWorkspace = fileCfg.AgentWorkspace
				}
				if cfg.AgentVersionCommand == "" {
					cfg.AgentVersionCommand = fileCfg.AgentVersionCommand
				}
				if cfg.NotifyBufferSeconds == 0 {
					cfg.NotifyBufferSeconds = fileCfg.NotifyBufferSeconds
				}
				if cfg.PermissionBufferSeconds == 0 {
					cfg.PermissionBufferSeconds = fileCfg.PermissionBufferSeconds
				}
				if cfg.IdleBufferSeconds == 0 {
					cfg.IdleBufferSeconds = fileCfg.IdleBufferSeconds
				}
				cfg.Workflow = mergeWorkflowConfig(cfg.Workflow, fileCfg.Workflow)
			}
		}
	}

	if cfg.DefaultAgent == "" {
		cfg.DefaultAgent = "csc"
	}

	// If AgentCommand is not set but AgentPath is, derive commands from AgentPath
	if cfg.AgentCommand == "" && cfg.AgentPath != "" {
		cfg.AgentCommand = cfg.AgentPath + " serve"
		if cfg.AgentVersionCommand == "" {
			cfg.AgentVersionCommand = cfg.AgentPath + " --version"
		}
	}

	// Environment variable overrides config file for buffer seconds
	if env := platform.Getenv("CS_CLOUD_NOTIFY_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.NotifyBufferSeconds = v
		}
	}
	if cfg.NotifyBufferSeconds == 0 {
		cfg.NotifyBufferSeconds = 60
	}

	if env := platform.Getenv("CS_CLOUD_PERMISSION_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.PermissionBufferSeconds = v
		}
	}
	if cfg.PermissionBufferSeconds == 0 {
		cfg.PermissionBufferSeconds = 5
	}

	if env := platform.Getenv("CS_CLOUD_IDLE_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.IdleBufferSeconds = v
		}
	}
	if cfg.IdleBufferSeconds == 0 {
		cfg.IdleBufferSeconds = 30
	}

	// If the workflow multica base URL is not explicitly configured and we
	// have a CoStrict base URL, derive the test/enterprise workflow backend
	// URL from it. Explicit env/file config always wins. The URL is not
	// required at config load time so that commands like stop/restart work
	// without a network configuration; workflow components validate it when
	// they start.
	if cfg.Workflow.MulticaBaseURL == "" && cfg.BaseURL != "" {
		cfg.Workflow.MulticaBaseURL = strings.TrimRight(cfg.BaseURL, "/") + "/workflow-backend"
	}

	return cfg, nil
}

// parsePositiveDuration parses a non-empty duration string and returns the
// value only when it is strictly positive. Zero/negative durations are invalid
// for loop intervals (a non-positive heartbeat/sync/gc interval makes the
// runtime loop return immediately), so they are ignored like parse errors.
func parsePositiveDuration(v string) (time.Duration, bool) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

func mergeWorkflowConfig(current, file workflow.Config) workflow.Config {
	defaults := workflow.DefaultConfig()
	if file.MulticaBaseURL != "" && current.MulticaBaseURL == "" {
		current.MulticaBaseURL = file.MulticaBaseURL
	}
	if file.WorkspacesRoot != "" && current.WorkspacesRoot == defaults.WorkspacesRoot {
		current.WorkspacesRoot = file.WorkspacesRoot
	}
	if file.CacheDir != "" && current.CacheDir == defaults.CacheDir {
		current.CacheDir = file.CacheDir
	}
	if file.SyncInterval != 0 && current.SyncInterval == defaults.SyncInterval {
		current.SyncInterval = file.SyncInterval
	}
	if file.GCInterval != 0 && current.GCInterval == defaults.GCInterval {
		current.GCInterval = file.GCInterval
	}
	// GC TTLs: file overrides only when current still equals the default (env
	// wins over file, mirroring GCInterval). GCEnabled is OR-ed — a file that
	// explicitly sets it false only takes effect when env hasn't set it (the env
	// path above leaves the default true when unset, so we cannot distinguish
	// "default true" from "env true" here; treat file false as authoritative).
	if file.GCTTL != 0 && current.GCTTL == defaults.GCTTL {
		current.GCTTL = file.GCTTL
	}
	if file.GCOrphanTTL != 0 && current.GCOrphanTTL == defaults.GCOrphanTTL {
		current.GCOrphanTTL = file.GCOrphanTTL
	}
	if file.GCArtifactTTL != 0 && current.GCArtifactTTL == defaults.GCArtifactTTL {
		current.GCArtifactTTL = file.GCArtifactTTL
	}
	if len(file.GCArtifactPatterns) > 0 && stringSlicesEqual(current.GCArtifactPatterns, defaults.GCArtifactPatterns) {
		current.GCArtifactPatterns = file.GCArtifactPatterns
	}
	if !file.GCEnabled {
		current.GCEnabled = false
	}
	if file.HeartbeatInterval != 0 && current.HeartbeatInterval == defaults.HeartbeatInterval {
		current.HeartbeatInterval = file.HeartbeatInterval
	}
	if file.AgentTimeout != 0 && current.AgentTimeout == defaults.AgentTimeout {
		current.AgentTimeout = file.AgentTimeout
	}
	if file.MaxConcurrentTasks != 0 && current.MaxConcurrentTasks == defaults.MaxConcurrentTasks {
		current.MaxConcurrentTasks = file.MaxConcurrentTasks
	}
	if len(file.AllowedAgents) > 0 && stringSlicesEqual(current.AllowedAgents, defaults.AllowedAgents) {
		current.AllowedAgents = file.AllowedAgents
	}
	return current
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func configFilePath() (string, error) {
	return filepath.Join(platform.AppDir(), "config.json"), nil
}
