package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cs-cloud/internal/platform"
)

// isolatedConfig 创建隔离的临时 data dir，写入指定的 config.json 内容，
// 返回 cleanup 函数。所有需要读取配置文件的测试都应使用此工具。
func isolatedConfig(t *testing.T, content string) {
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() { platform.SetDataDir("") })

	cfgDir := filepath.Join(dir, "cs-cloud")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Provide a default CoStrict base URL so Load() does not fail on the
	// required workflow multica URL. Tests that need to control this can
	// override the env var explicitly.
	t.Setenv("COSTRICT_BASE_URL", "https://example.costrict.local")
}

func TestLoad_AgentPathFromEnv(t *testing.T) {
	// 写入空配置，避免开发机上的真实 config.json 干扰
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_AGENT_PATH", "/custom/bin/csc")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentPath != "/custom/bin/csc" {
		t.Errorf("AgentPath = %q, want %q", cfg.AgentPath, "/custom/bin/csc")
	}
}

func TestLoad_AgentPathDerivesCommands(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_AGENT_PATH", "/usr/local/bin/csc")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentCommand != "/usr/local/bin/csc serve" {
		t.Errorf("AgentCommand = %q, want %q", cfg.AgentCommand, "/usr/local/bin/csc serve")
	}
	if cfg.AgentVersionCommand != "/usr/local/bin/csc --version" {
		t.Errorf("AgentVersionCommand = %q, want %q", cfg.AgentVersionCommand, "/usr/local/bin/csc --version")
	}
}

func TestLoad_AgentPathDoesNotOverrideExplicitCommand(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_AGENT_PATH", "/some/bin/csc")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "csc serve --extra-flag")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// AgentCommand 已显式设置，不应被派生逻辑覆盖
	if cfg.AgentCommand != "csc serve --extra-flag" {
		t.Errorf("AgentCommand = %q, want %q", cfg.AgentCommand, "csc serve --extra-flag")
	}
	// AgentVersionCommand 的派生也受 AgentCommand=="" 保护（同一 if 块内），
	if cfg.AgentVersionCommand != "" {
		t.Errorf("AgentVersionCommand = %q, want empty since AgentCommand is explicitly set", cfg.AgentVersionCommand)
	}
}

func TestLoad_AgentPathDoesNotDeriveWhenEmpty(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_AGENT_PATH", "")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")
	t.Setenv("CS_BRIDGE_AUTO_UPGRADE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentCommand != "" {
		t.Errorf("AgentCommand = %q, want empty when AgentPath is empty", cfg.AgentCommand)
	}
	if cfg.AgentVersionCommand != "" {
		t.Errorf("AgentVersionCommand = %q, want empty when AgentPath is empty", cfg.AgentVersionCommand)
	}
}

func TestLoad_AgentPathFromConfigFile(t *testing.T) {
	isolatedConfig(t, `{"agent_path":"/from/file/csc"}`)

	t.Setenv("CS_BRIDGE_AGENT_PATH", "")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")
	t.Setenv("CS_BRIDGE_AUTO_UPGRADE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentPath != "/from/file/csc" {
		t.Errorf("AgentPath = %q, want %q", cfg.AgentPath, "/from/file/csc")
	}
	// AgentCommand 未设置，应从 AgentPath 派生
	if cfg.AgentCommand != "/from/file/csc serve" {
		t.Errorf("AgentCommand = %q, want %q", cfg.AgentCommand, "/from/file/csc serve")
	}
}

func TestLoad_AgentPathEnvOverridesConfigFile(t *testing.T) {
	isolatedConfig(t, `{"agent_path":"/from/file/csc"}`)

	t.Setenv("CS_BRIDGE_AGENT_PATH", "/from/env/csc")
	t.Setenv("CS_BRIDGE_AGENT_COMMAND", "")
	t.Setenv("CS_BRIDGE_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")
	t.Setenv("CS_BRIDGE_AUTO_UPGRADE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentPath != "/from/env/csc" {
		t.Errorf("AgentPath = %q, want %q (env should override file)", cfg.AgentPath, "/from/env/csc")
	}
}

func TestLoad_IdleBufferSeconds_DefaultAndEnv(t *testing.T) {
	// Default when neither env nor file sets it
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_IDLE_BUFFER_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleBufferSeconds != 30 {
		t.Errorf("default IdleBufferSeconds = %d, want 30", cfg.IdleBufferSeconds)
	}

	// Env override
	t.Setenv("CS_BRIDGE_IDLE_BUFFER_SECONDS", "120")
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.IdleBufferSeconds != 120 {
		t.Errorf("env IdleBufferSeconds = %d, want 120", cfg2.IdleBufferSeconds)
	}
}

func TestLoadWorkflowConfigFromEnv(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_WORKFLOW_WORKSPACES_ROOT", "/tmp/wf-workspaces")
	t.Setenv("CS_BRIDGE_WORKFLOW_SYNC_INTERVAL", "10m")
	t.Setenv("CS_BRIDGE_WORKFLOW_MAX_CONCURRENT_TASKS", "42")

	// Clear other workflow env vars so defaults don't interfere with assertions.
	t.Setenv("CS_BRIDGE_WORKFLOW_MULTICA_BASE_URL", "")
	t.Setenv("CS_BRIDGE_WORKFLOW_CACHE_DIR", "")
	t.Setenv("CS_BRIDGE_WORKFLOW_GC_INTERVAL", "")
	t.Setenv("CS_BRIDGE_WORKFLOW_AGENT_TIMEOUT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Workflow.WorkspacesRoot != "/tmp/wf-workspaces" {
		t.Fatalf("WorkspacesRoot = %q", cfg.Workflow.WorkspacesRoot)
	}
	if cfg.Workflow.SyncInterval != 10*time.Minute {
		t.Fatalf("SyncInterval = %v", cfg.Workflow.SyncInterval)
	}
	if cfg.Workflow.MaxConcurrentTasks != 42 {
		t.Fatalf("MaxConcurrentTasks = %d", cfg.Workflow.MaxConcurrentTasks)
	}
}

func TestLoad_WorkflowHeartbeatIntervalFromConfigFile(t *testing.T) {
	isolatedConfig(t, `{"workflow":{"heartbeat_interval":60000000000}}`)
	t.Setenv("CS_BRIDGE_WORKFLOW_HEARTBEAT_INTERVAL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Workflow.HeartbeatInterval != time.Minute {
		t.Fatalf("HeartbeatInterval = %v, want %v", cfg.Workflow.HeartbeatInterval, time.Minute)
	}
}

func TestLoad_WorkflowAllowedAgentsEnvOverridesConfigFileWithSameLengthAsDefault(t *testing.T) {
	isolatedConfig(t, `{"workflow":{"allowed_agents":["file-a","file-b"]}}`)
	t.Setenv("CS_BRIDGE_WORKFLOW_ALLOWED_AGENTS", "env-a,env-b,env-c,env-d,env-e")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := []string{"env-a", "env-b", "env-c", "env-d", "env-e"}
	if len(cfg.Workflow.AllowedAgents) != len(want) {
		t.Fatalf("AllowedAgents = %#v, want %#v", cfg.Workflow.AllowedAgents, want)
	}
	for i := range want {
		if cfg.Workflow.AllowedAgents[i] != want[i] {
			t.Fatalf("AllowedAgents = %#v, want %#v", cfg.Workflow.AllowedAgents, want)
		}
	}
}

func TestLoad_WorkflowMulticaBaseURLDerivedFromBaseURL(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("COSTRICT_BASE_URL", "https://zgsmtest.cn:30443")
	t.Setenv("CS_BRIDGE_WORKFLOW_MULTICA_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := "https://zgsmtest.cn:30443/workflow-backend"
	if cfg.Workflow.MulticaBaseURL != want {
		t.Fatalf("MulticaBaseURL = %q, want %q", cfg.Workflow.MulticaBaseURL, want)
	}
}

func TestLoad_WorkflowMulticaBaseURLFromEnvOverridesBaseURL(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("COSTRICT_BASE_URL", "https://zgsmtest.cn:30443")
	t.Setenv("CS_BRIDGE_WORKFLOW_MULTICA_BASE_URL", "https://explicit.example.com/multica")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := "https://explicit.example.com/multica"
	if cfg.Workflow.MulticaBaseURL != want {
		t.Fatalf("MulticaBaseURL = %q, want %q", cfg.Workflow.MulticaBaseURL, want)
	}
}

func TestLoad_WorkflowMulticaBaseURLDefaultWhenNoBaseURL(t *testing.T) {
	isolatedConfig(t, `{}`)
	// Ensure neither explicit env nor derivation source is present.
	t.Setenv("COSTRICT_BASE_URL", "")
	t.Setenv("CS_BRIDGE_WORKFLOW_MULTICA_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Workflow.MulticaBaseURL != "" {
		t.Fatalf("MulticaBaseURL = %q, want empty", cfg.Workflow.MulticaBaseURL)
	}
}

func TestLoad_WorkflowMulticaBaseURLFromConfigFilePreventsDerivation(t *testing.T) {
	isolatedConfig(t, `{"workflow":{"multica_base_url":"https://file.example.com"}}`)
	t.Setenv("COSTRICT_BASE_URL", "https://zgsmtest.cn:30443")
	t.Setenv("CS_BRIDGE_WORKFLOW_MULTICA_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := "https://file.example.com"
	if cfg.Workflow.MulticaBaseURL != want {
		t.Fatalf("MulticaBaseURL = %q, want %q", cfg.Workflow.MulticaBaseURL, want)
	}
}

func TestLoad_APIKeyFromEnv(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_API_KEY", "env-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "env-secret" {
		t.Fatalf("APIKey = %q, want %q", cfg.APIKey, "env-secret")
	}
}

func TestLoad_APIKeyFromFile(t *testing.T) {
	isolatedConfig(t, `{"api_key":"file-secret"}`)
	t.Setenv("CS_BRIDGE_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "file-secret" {
		t.Fatalf("APIKey = %q, want %q", cfg.APIKey, "file-secret")
	}
}

func TestLoad_APIKeyEnvOverridesFile(t *testing.T) {
	isolatedConfig(t, `{"api_key":"file-secret"}`)
	t.Setenv("CS_BRIDGE_API_KEY", "env-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "env-secret" {
		t.Fatalf("APIKey = %q, want %q (env should win over file)", cfg.APIKey, "env-secret")
	}
}

func TestLoad_APIKeyEmptyByDefault(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty (default = no auth)", cfg.APIKey)
	}
}

// TestLoad_LegacyCSCloudEnvAliasesCovered exercises the back-compat fallback:
// every renamed config var should still be readable via its legacy
// CS_CLOUD_* alias when the CS_BRIDGE_* name is unset. This is the
// cross-version compat contract for existing deployments.
func TestLoad_LegacyCSCloudEnvAliasesCovered(t *testing.T) {
	isolatedConfig(t, `{}`)
	// Pin all legacy vars; unset every new name to prove the alias path wins.
	t.Setenv("CS_BRIDGE_AGENT_PATH", "")
	t.Setenv("CS_BRIDGE_DEFAULT_AGENT", "")
	t.Setenv("CS_BRIDGE_API_KEY", "")
	t.Setenv("CS_BRIDGE_AUTO_UPGRADE", "")
	t.Setenv("CS_BRIDGE_NOTIFY_BUFFER_SECONDS", "")

	t.Setenv("CS_CLOUD_AGENT_PATH", "/legacy/bin/csc")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "csc")
	t.Setenv("CS_CLOUD_API_KEY", "legacy-secret")
	t.Setenv("CS_CLOUD_AUTO_UPGRADE", "true")
	t.Setenv("CS_CLOUD_NOTIFY_BUFFER_SECONDS", "99")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.AgentPath != "/legacy/bin/csc" {
		t.Errorf("AgentPath = %q, want legacy %q", cfg.AgentPath, "/legacy/bin/csc")
	}
	if cfg.DefaultAgent != "csc" {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "csc")
	}
	if cfg.APIKey != "legacy-secret" {
		t.Errorf("APIKey = %q, want legacy %q", cfg.APIKey, "legacy-secret")
	}
	if !cfg.AutoUpgrade {
		t.Errorf("AutoUpgrade = false, want true")
	}
	if cfg.NotifyBufferSeconds != 99 {
		t.Errorf("NotifyBufferSeconds = %d, want 99", cfg.NotifyBufferSeconds)
	}
}

// TestLoad_NewNameOverridesLegacy proves the new CS_BRIDGE_* name takes
// priority when both are set (so operators can migrate without surprises).
func TestLoad_NewNameOverridesLegacy(t *testing.T) {
	isolatedConfig(t, `{}`)
	t.Setenv("CS_BRIDGE_API_KEY", "new-secret")
	t.Setenv("CS_CLOUD_API_KEY", "legacy-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "new-secret" {
		t.Fatalf("APIKey = %q, want new-name %q (env should win over legacy)", cfg.APIKey, "new-secret")
	}
}
