package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"cs-cloud/internal/platform"
)

func Load() (*Config, error) {
	cfg := &Config{
		CloudBaseURL:   platform.Getenv("CLOUD_BASE_URL"),
		BaseURL:        platform.Getenv("COSTRICT_BASE_URL"),
		DefaultShell:   platform.Getenv("CS_CLOUD_SHELL"),
		DefaultAgent:   platform.Getenv("CS_CLOUD_DEFAULT_AGENT"),
		AgentCommand:   platform.Getenv("CS_CLOUD_AGENT_COMMAND"),
		AgentVersionCommand: platform.Getenv("CS_CLOUD_AGENT_VERSION_COMMAND"),
	}

	if cfg.CloudBaseURL == "" {
		cfg.CloudBaseURL = platform.Getenv("COSTRICT_CLOUD_BASE_URL")
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
			}
		}
	}

	if cfg.DefaultAgent == "" {
		cfg.DefaultAgent = "csc"
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

	return cfg, nil
}

func configFilePath() (string, error) {
	return filepath.Join(platform.AppDir(), "config.json"), nil
}
