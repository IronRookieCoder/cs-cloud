package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"cs-cloud/internal/app"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/updater"
	"cs-cloud/internal/version"
)

func upgradeCmd(a *app.App) error {
	autoYes := false
	args := commandArgs()
	for _, arg := range args[1:] {
		if arg == "-y" || arg == "--yes" {
			autoYes = true
		}
	}

	logger.Init(logger.Config{
		Dir:        a.RootDir(),
		MaxSizeMB:  100,
		MaxAgeDays: 7,
		MaxBackups: 10,
		Console:    false,
	})
	defer logger.Sync()

	mgr := updater.NewManager(a.CloudBaseURL(), a.RootDir())

	printTitle("cs-cloud upgrade")
	printInfo("Checking for updates...")

	result, err := mgr.CheckNow(context.Background())
	if err != nil {
		printError("Check failed: %v", err)
		return err
	}

	if !result.CanUpdate {
		printSuccess("Already up to date (%s)", version.Get())
		return nil
	}

	fmt.Print(renderKV([][2]string{
		{"current", version.Get()},
		{"latest", result.Version},
		{"changelog", result.Changelog},
		{"platform", updater.PlatformString()},
	}))

	if !confirmPrompt(autoYes, "Upgrade to "+result.Version+"?") {
		printInfo("Upgrade cancelled")
		return nil
	}

	printInfo("Downloading and applying upgrade...")
	if err := mgr.Apply(context.Background(), ""); err != nil {
		printError("Upgrade failed: %v", err)
		return err
	}

	if err := mgr.SwapStagedBinary(); err != nil {
		printError("Binary swap failed: %v", err)
		return err
	}

	printSuccess("Upgraded to %s", result.Version)

	isRunning, _, _ := a.IsRunning()
	if !isRunning {
		printInfo("Daemon is not running, skip restart")
		return nil
	}

	if !confirmPrompt(autoYes, "Restart daemon now?") {
		printInfo("Restart the daemon manually to use the new version")
		return nil
	}

	os.Setenv("CS_CLOUD_SKIP_UPDATE_CHECK", "true")
	printInfo("Restarting daemon...")
	if err := restart(a); err != nil {
		printError("Restart failed: %v", err)
		return err
	}
	printSuccess("Daemon restarted")
	return nil
}

func confirmPrompt(autoYes bool, prompt string) bool {
	if autoYes {
		fmt.Printf("%s [y/N]: y\n", bold(prompt))
		return true
	}
	fmt.Printf("%s [y/N]: ", bold(prompt))
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}
