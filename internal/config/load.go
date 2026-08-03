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
		DefaultShell:        platform.GetenvCompat("CS_BRIDGE_SHELL", "CS_CLOUD_SHELL"),
		DefaultAgent:        platform.GetenvCompat("CS_BRIDGE_DEFAULT_AGENT", "CS_CLOUD_DEFAULT_AGENT"),
		AgentPath:           platform.GetenvCompat("CS_BRIDGE_AGENT_PATH", "CS_CLOUD_AGENT_PATH"),
		AgentCommand:        platform.GetenvCompat("CS_BRIDGE_AGENT_COMMAND", "CS_CLOUD_AGENT_COMMAND"),
		AgentVersionCommand: platform.GetenvCompat("CS_BRIDGE_AGENT_VERSION_COMMAND", "CS_CLOUD_AGENT_VERSION_COMMAND"),
		APIKey:              platform.GetenvCompat("CS_BRIDGE_API_KEY", "CS_CLOUD_API_KEY"),
		Workflow:            workflow.DefaultConfig(),
	}

	if cfg.CloudBaseURL == "" {
		cfg.CloudBaseURL = platform.Getenv("COSTRICT_CLOUD_BASE_URL")
	}

	// Workflow config from environment variables.
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_BACKEND_BASE_URL", "CS_CLOUD_WORKFLOW_BACKEND_BASE_URL"); v != "" {
		cfg.Workflow.BackendBaseURL = v
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_WORKSPACES_ROOT", "CS_CLOUD_WORKFLOW_WORKSPACES_ROOT"); v != "" {
		cfg.Workflow.WorkspacesRoot = v
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_CACHE_DIR", "CS_CLOUD_WORKFLOW_CACHE_DIR"); v != "" {
		cfg.Workflow.CacheDir = v
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_SYNC_INTERVAL", "CS_CLOUD_WORKFLOW_SYNC_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.SyncInterval = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_INTERVAL", "CS_CLOUD_WORKFLOW_GC_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCInterval = d
		}
	}
	// envGCSet tracks whether the env explicitly set GC state, so the file
	// merge below knows whether the file's gc_enabled is allowed to win. With-
	// out it, json.Unmarshal maps a missing workflow.gc_enabled key to false,
	// which would silently disable GC for every config file that doesn't write
	// the key explicitly (CodeRabbit PR #27 comment 10).
	envGCSet := false
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_ENABLED", "CS_CLOUD_WORKFLOW_GC_ENABLED"); v != "" {
		cfg.Workflow.GCEnabled = v == "true" || v == "1" || v == "yes"
		envGCSet = true
	} else if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_DISABLED", "CS_CLOUD_WORKFLOW_GC_DISABLED"); v != "" {
		// Any explicit value is an env-level override: truthy (true/1/yes) →
		// disabled, anything else (false/0/no) → enabled. This ensures
		// GC_DISABLED=false wins over a file-level gc_enabled:false, honoring
		// the operator's explicit opt-in (CodeRabbit PR #27 follow-up).
		cfg.Workflow.GCEnabled = !(v == "true" || v == "1" || v == "yes")
		envGCSet = true
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_TTL", "CS_CLOUD_WORKFLOW_GC_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCTTL = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_ORPHAN_TTL", "CS_CLOUD_WORKFLOW_GC_ORPHAN_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCOrphanTTL = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_ARTIFACT_TTL", "CS_CLOUD_WORKFLOW_GC_ARTIFACT_TTL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.GCArtifactTTL = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_GC_ARTIFACT_PATTERNS", "CS_CLOUD_WORKFLOW_GC_ARTIFACT_PATTERNS"); v != "" {
		cfg.Workflow.GCArtifactPatterns = strings.Split(v, ",")
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_HEARTBEAT_INTERVAL", "CS_CLOUD_WORKFLOW_HEARTBEAT_INTERVAL"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.HeartbeatInterval = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_AGENT_TIMEOUT", "CS_CLOUD_WORKFLOW_AGENT_TIMEOUT"); v != "" {
		if d, ok := parsePositiveDuration(v); ok {
			cfg.Workflow.AgentTimeout = d
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_MAX_CONCURRENT_TASKS", "CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workflow.MaxConcurrentTasks = n
		}
	}
	if v := platform.GetenvCompat("CS_BRIDGE_WORKFLOW_ALLOWED_AGENTS", "CS_CLOUD_WORKFLOW_ALLOWED_AGENTS"); v != "" {
		cfg.Workflow.AllowedAgents = strings.Split(v, ",")
	}

	if envJSON := platform.GetenvCompat("CS_BRIDGE_AGENT_ENV", "CS_CLOUD_AGENT_ENV"); envJSON != "" {
		var env map[string]string
		if err := json.Unmarshal([]byte(envJSON), &env); err == nil {
			cfg.AgentEnv = env
		}
	}

	if env := platform.GetenvCompat("CS_BRIDGE_AUTO_UPGRADE", "CS_CLOUD_AUTO_UPGRADE"); env != "" {
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
				if cfg.APIKey == "" {
					cfg.APIKey = fileCfg.APIKey
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

				// GCEnabled file-merge: a config file that explicitly writes
				// workflow.gc_enabled should override the default true, but
				// ONLY when the env hasn't already set it (env wins over file).
				// json.Unmarshal maps a missing key to false, so detect presence
				// with a *bool probe rather than treating default-false as an
				// explicit opt-out — otherwise every config file that omits the
				// key silently disables GC (CodeRabbit PR #27 comment 10).
				if !envGCSet {
					var probe struct {
						Workflow struct {
							GCEnabled *bool `json:"gc_enabled"`
						} `json:"workflow"`
					}
					if err := json.Unmarshal(b, &probe); err == nil && probe.Workflow.GCEnabled != nil {
						cfg.Workflow.GCEnabled = *probe.Workflow.GCEnabled
					}
				}
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
	if env := platform.GetenvCompat("CS_BRIDGE_NOTIFY_BUFFER_SECONDS", "CS_CLOUD_NOTIFY_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.NotifyBufferSeconds = v
		}
	}
	if cfg.NotifyBufferSeconds == 0 {
		cfg.NotifyBufferSeconds = 60
	}

	if env := platform.GetenvCompat("CS_BRIDGE_PERMISSION_BUFFER_SECONDS", "CS_CLOUD_PERMISSION_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.PermissionBufferSeconds = v
		}
	}
	if cfg.PermissionBufferSeconds == 0 {
		cfg.PermissionBufferSeconds = 5
	}

	if env := platform.GetenvCompat("CS_BRIDGE_IDLE_BUFFER_SECONDS", "CS_CLOUD_IDLE_BUFFER_SECONDS"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.IdleBufferSeconds = v
		}
	}
	if cfg.IdleBufferSeconds == 0 {
		cfg.IdleBufferSeconds = 30
	}

	// If the workflow server base URL is not explicitly configured and we
	// have a CoStrict base URL, derive the test/enterprise workflow backend
	// URL from it. Explicit env/file config always wins. The URL is not
	// required at config load time so that commands like stop/restart work
	// without a network configuration; workflow components validate it when
	// they start.
	// Derive the workflow backend base URL from the same base-URL chain the
	// cloud client uses — config base_url, then the login credential, then
	// the built-in default — so logging into an environment is enough for
	// the daemon to reach that environment's workflow backend. Explicit
	// configuration (CS_CLOUD_WORKFLOW_BACKEND_BASE_URL env or the config
	// file's workflow.backend_base_url) always wins and never reaches this
	// derivation.
	if cfg.Workflow.BackendBaseURL == "" {
		base := cfg.BaseURL
		if base == "" {
			base = credentialsBaseURL()
		}
		if base == "" {
			base = platform.DefaultCloudBaseURL
		}
		cfg.Workflow.BackendBaseURL = strings.TrimRight(base, "/") + "/workflow-backend"
	}

	return cfg, nil
}

// credentialsBaseURL returns the base_url of the logged-in credential
// (share/auth.json), or "" when the file is missing, unreadable, or has no
// base_url. It reads the file directly instead of going through
// internal/provider because provider imports internal/cloud, which imports
// this package — importing provider here would create an import cycle.
func credentialsBaseURL() string {
	data, err := os.ReadFile(filepath.Join(platform.CoStrictShareDir(), "auth.json"))
	if err != nil {
		return ""
	}
	var cred struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return ""
	}
	return strings.TrimSpace(cred.BaseURL)
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
	if file.BackendBaseURL != "" && current.BackendBaseURL == "" {
		current.BackendBaseURL = file.BackendBaseURL
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
	// wins over file, mirroring GCInterval). GCEnabled is handled in Load()
	// (see the *bool probe there) — not here, because json.Unmarshal zeroes a
	// missing key and we need to distinguish absent from explicit-false.
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
	// GCEnabled is intentionally NOT handled here: json.Unmarshal maps a
	// missing workflow.gc_enabled key to false, so merging in this function
	// (which runs unconditionally) would silently disable GC for any file
	// that doesn't write the key. The override lives in Load() instead,
	// where a *bool probe distinguishes explicit-false from absent.
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
