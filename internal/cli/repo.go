package cli

import (
	"fmt"
	"os"
	"os/exec"

	"cs-cloud/internal/app"
)

// repoCmd implements `cs-cloud repo <subcommand>`.
func repoCmd(a *app.App, args []string) error {
	_ = a
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud repo checkout <url> [--ref <branch>]")
	}
	switch args[0] {
	case "checkout":
		return repoCheckout(args[1:])
	default:
		return fmt.Errorf("unknown repo command: %s", args[0])
	}
}

// repoCheckout: `cs-cloud repo checkout <url> [--ref <branch>]`
// Clones a repository into the current working directory. Used by agents to
// check out repos on demand (e.g. a dependency repo referenced in the task).
func repoCheckout(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud repo checkout <url> [--ref <branch>]")
	}
	repoURL := args[0]
	var ref string
	for i := 1; i < len(args); i++ {
		if args[i] == "--ref" && i+1 < len(args) {
			ref = args[i+1]
			i++
		}
	}
	cloneArgs := []string{"clone"}
	if ref != "" {
		cloneArgs = append(cloneArgs, "--branch", ref)
	}
	cloneArgs = append(cloneArgs, repoURL)
	cmd := exec.Command("git", cloneArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
