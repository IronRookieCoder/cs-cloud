package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.SyncInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.GCInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_AGENT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.AgentTimeout = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workflow.MaxConcurrentTasks = n
		}
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

	return cfg, nil
}

func mergeWorkflowConfig(current, file workflow.Config) workflow.Config {
	if file.MulticaBaseURL != "" {
		current.MulticaBaseURL = file.MulticaBaseURL
	}
	if file.WorkspacesRoot != "" {
		current.WorkspacesRoot = file.WorkspacesRoot
	}
	if file.CacheDir != "" {
		current.CacheDir = file.CacheDir
	}
	if file.SyncInterval != 0 {
		current.SyncInterval = file.SyncInterval
	}
	if file.GCInterval != 0 {
		current.GCInterval = file.GCInterval
	}
	if file.AgentTimeout != 0 {
		current.AgentTimeout = file.AgentTimeout
	}
	if file.MaxConcurrentTasks != 0 {
		current.MaxConcurrentTasks = file.MaxConcurrentTasks
	}
	return current
}

func configFilePath() (string, error) {
	return filepath.Join(platform.AppDir(), "config.json"), nil
}
