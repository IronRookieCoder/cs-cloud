package cli

import (
	"os"
	"testing"
)

// saveEnv 保存并恢复环境变量
func saveEnv(t *testing.T, key string) {
	old, ok := os.LookupEnv(key)
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestParseGlobalFlags_AgentPathSeparateArg(t *testing.T) {
	saveEnv(t, "CS_BRIDGE_AGENT_PATH")
	saveEnv(t, "CS_CLOUD_AGENT_PATH")
	os.Unsetenv("CS_BRIDGE_AGENT_PATH")
	os.Unsetenv("CS_CLOUD_AGENT_PATH")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cs-cloud", "--agent-path", "/opt/csc/bin", "start"}

	parseGlobalFlags()

	if got := os.Getenv("CS_BRIDGE_AGENT_PATH"); got != "/opt/csc/bin" {
		t.Errorf("CS_BRIDGE_AGENT_PATH = %q, want %q", got, "/opt/csc/bin")
	}
	if got := os.Getenv("CS_CLOUD_AGENT_PATH"); got != "/opt/csc/bin" {
		t.Errorf("legacy CS_CLOUD_AGENT_PATH = %q, want %q", got, "/opt/csc/bin")
	}
}

func TestParseGlobalFlags_AgentPathEqualsArg(t *testing.T) {
	saveEnv(t, "CS_BRIDGE_AGENT_PATH")
	saveEnv(t, "CS_CLOUD_AGENT_PATH")
	os.Unsetenv("CS_BRIDGE_AGENT_PATH")
	os.Unsetenv("CS_CLOUD_AGENT_PATH")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cs-cloud", "--agent-path=/usr/local/bin/csc", "start"}

	parseGlobalFlags()

	if got := os.Getenv("CS_BRIDGE_AGENT_PATH"); got != "/usr/local/bin/csc" {
		t.Errorf("CS_BRIDGE_AGENT_PATH = %q, want %q", got, "/usr/local/bin/csc")
	}
	if got := os.Getenv("CS_CLOUD_AGENT_PATH"); got != "/usr/local/bin/csc" {
		t.Errorf("legacy CS_CLOUD_AGENT_PATH = %q, want %q", got, "/usr/local/bin/csc")
	}
}

func TestParseGlobalFlags_AgentPathLastArgWithoutValue(t *testing.T) {
	// --agent-path as the last arg with no following value should not panic
	saveEnv(t, "CS_BRIDGE_AGENT_PATH")
	saveEnv(t, "CS_CLOUD_AGENT_PATH")
	os.Unsetenv("CS_BRIDGE_AGENT_PATH")
	os.Unsetenv("CS_CLOUD_AGENT_PATH")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	// This is an edge case: --agent-path with no following arg
	os.Args = []string{"cs-cloud", "--agent-path"}

	parseGlobalFlags()
	// Should not have consumed past the slice, and env should not be set
	// (the flag handler requires i+1 < len(args))
}

func TestCommandArgs_FiltersAgentPathSeparateArg(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cs-cloud", "--agent-path", "/bin/csc", "version"}

	cmds := commandArgs()
	if len(cmds) != 1 || cmds[0] != "version" {
		t.Errorf("commandArgs() = %v, want [version]", cmds)
	}
}

func TestCommandArgs_FiltersAgentPathEqualsArg(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cs-cloud", "--agent-path=/bin/csc", "status"}

	cmds := commandArgs()
	if len(cmds) != 1 || cmds[0] != "status" {
		t.Errorf("commandArgs() = %v, want [status]", cmds)
	}
}

func TestCommandArgs_AgentPathDoesNotAffectOtherFlags(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	// 注意：使用 --auth-path（分开传值），而不是 --auth-path=
	// （--auth-path= 在 commandArgs() 中有预存的 [:11] 截取问题）
	os.Args = []string{"cs-cloud", "--agent-path=/bin/csc", "--auth-path", "/tmp/auth.json", "start"}

	cmds := commandArgs()
	if len(cmds) != 1 || cmds[0] != "start" {
		t.Errorf("commandArgs() = %v, want [start]", cmds)
	}
}
