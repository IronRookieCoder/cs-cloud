package config

type Config struct {
	CloudBaseURL   string            `json:"cloud_base_url"`
	BaseURL        string            `json:"base_url"`
	DefaultShell   string            `json:"default_shell"`
	DefaultAgent   string            `json:"default_agent"`
	AgentCommand   string            `json:"agent_command"`
	AgentVersionCommand string        `json:"agent_version_command,omitempty"`
	AgentEnv       map[string]string `json:"agent_env,omitempty"`
	AgentWorkspace string            `json:"agent_workspace,omitempty"`
	AutoUpgrade           bool              `json:"auto_upgrade"`
	NotifyBufferSeconds      int               `json:"notify_buffer_seconds"`
	PermissionBufferSeconds  int               `json:"permission_buffer_seconds"`
	Runtime               RuntimeConfig     `json:"runtime"`
}

type RuntimeConfig struct {
	AllowAbsolutePaths bool     `json:"allow_absolute_paths"`
	MaxListDepth       int      `json:"max_list_depth"`
	AllowedOperations  []string `json:"allowed_operations"`
	BlacklistCount     int      `json:"blacklist_count"`
	WhitelistEnabled   bool     `json:"whitelist_enabled"`
}
