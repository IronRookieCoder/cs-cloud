package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"cs-cloud/internal/app"
	agentcs "cs-cloud/internal/agent/cs"
	agentcsc "cs-cloud/internal/agent/csc"
	"cs-cloud/internal/version"
)

func printVersion(a *app.App) {
	printTitle("cs-cloud")
	fmt.Print(renderKV([][2]string{
		{"version", version.Get()},
		{"commit", version.Commit},
		{"built", version.BuildTime},
		{"go", version.GoVersion},
		{"platform", version.Platform},
	}))

	cfg := a.Config()
	if cfg == nil {
		return
	}

	agentType := cfg.DefaultAgent
	if agentType == "" {
		agentType = "cs"
	}

	// Determine the agent CLI binary name
	agentCLI := agentcs.CLIBinary
	if agentType == "csc" {
		agentCLI = agentcsc.CLIBinary
	}

	// Get agent version by running the version command
	agentVersion := "unknown"
	versionCmd := cfg.AgentVersionCommand
	if versionCmd == "" {
		versionCmd = agentCLI + " --version"
	}
	if parts := strings.Fields(versionCmd); len(parts) > 0 {
		if out, err := exec.Command(parts[0], parts[1:]...).Output(); err == nil {
			agentVersion = strings.TrimSpace(string(out))
		}
	}

	fmt.Println()
	fmt.Print(renderKV([][2]string{
		{"agent", agentType},
		{"agent version", agentVersion},
	}))
}
