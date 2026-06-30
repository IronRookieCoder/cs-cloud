package config

import (
	"os"
	"path/filepath"
	"testing"

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
}

func TestLoad_AgentPathFromEnv(t *testing.T) {
	// 写入空配置，避免开发机上的真实 config.json 干扰
	isolatedConfig(t, `{}`)
	t.Setenv("CS_CLOUD_AGENT_PATH", "/custom/bin/csc")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")

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
	t.Setenv("CS_CLOUD_AGENT_PATH", "/usr/local/bin/csc")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")

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
	t.Setenv("CS_CLOUD_AGENT_PATH", "/some/bin/csc")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "csc serve --extra-flag")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")

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
	t.Setenv("CS_CLOUD_AGENT_PATH", "")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")
	t.Setenv("CS_CLOUD_AUTO_UPGRADE", "")

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

	t.Setenv("CS_CLOUD_AGENT_PATH", "")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")
	t.Setenv("CS_CLOUD_AUTO_UPGRADE", "")

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

	t.Setenv("CS_CLOUD_AGENT_PATH", "/from/env/csc")
	t.Setenv("CS_CLOUD_AGENT_COMMAND", "")
	t.Setenv("CS_CLOUD_AGENT_VERSION_COMMAND", "")
	t.Setenv("CS_CLOUD_DEFAULT_AGENT", "")
	t.Setenv("CS_CLOUD_AUTO_UPGRADE", "")

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
	t.Setenv("CS_CLOUD_IDLE_BUFFER_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleBufferSeconds != 30 {
		t.Errorf("default IdleBufferSeconds = %d, want 30", cfg.IdleBufferSeconds)
	}

	// Env override
	t.Setenv("CS_CLOUD_IDLE_BUFFER_SECONDS", "120")
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.IdleBufferSeconds != 120 {
		t.Errorf("env IdleBufferSeconds = %d, want 120", cfg2.IdleBufferSeconds)
	}
}
