package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/updater"
	"cs-cloud/internal/version"
)

const readyTimeout = 30 * time.Second

func start(a *app.App) error {
	// 如果是更新后的重启，跳过更新检查
	if os.Getenv("CS_CLOUD_SKIP_UPDATE_CHECK") == "true" {
		os.Unsetenv("CS_CLOUD_SKIP_UPDATE_CHECK") // 清除标记
	} else if !platform.NoAutoUpgrade() {
		// 检查并应用更新（如果有），除非用户指定了 --no-auto-upgrade
		if err := checkAndApplyUpdates(a); err != nil {
			printWarn("Update check failed: %v (continuing with current version)", err)
		}
	} else {
		printInfo("Auto-update disabled by --no-auto-upgrade flag")
	}

	running, pid, _ := a.DaemonStatus()
	if running {
		url, _ := a.ServerURL()
		printWarn("cs-cloud is already running")
		printKV("pid", fmt.Sprintf("%d", pid))
		printKV("url", url)
		printInfo("Use 'restart' command if you want to restart the service")
		return nil
	}

	if cleaned := a.ForceCleanupStale(); cleaned {
		printInfo("Cleaned up stale daemon process")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mode := parseMode()
	port, err := parsePort()
	if err != nil {
		return err
	}

	if mode == "cloud" {
		info, err := registerWithLogin(ctx, a)
		if err != nil {
			printRegDebugInfo(a)
			return err
		}
		printSuccess("Device registered")
		printKV("device_id", info.DeviceID)
		printKV("legacy_device_id", device.GetLegacyDeviceID())

		if err := device.ValidateDeviceToken(ctx, info); err != nil {
			if device.IsInvalidDeviceTokenError(err) {
		fmt.Println("device token is invalid, regenerating...")
			_ = device.ClearDevice()
			info, err = registerWithLogin(ctx, a)
			if err != nil {
				printRegDebugInfo(a)
				return err
			}
			printWarn("Device re-registered")
			printKV("device_id", info.DeviceID)
			printKV("legacy_device_id", device.GetLegacyDeviceID())
				if err := device.ValidateDeviceToken(ctx, info); err != nil {
					printRegDebugInfo(a)
					return err
				}
			} else {
				printRegDebugInfo(a)
				return err
			}
		}
		printSuccess("Device token validated")

		printInfo("Checking gateway connectivity...")
		if gwErr := device.CheckGatewayConnectivity(ctx, info); gwErr != nil {
			printError("Gateway connectivity check failed")
			printKV("error", gwErr.Error())
			printKV("hint", "Check your network connection and try again")
			return gwErr
		}
		printSuccess("Gateway connectivity OK")
	}

	_ = a.SaveMode(mode)

	printInfo("Starting daemon...")

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	nullFd, err := openNullDevice()
	if err != nil {
		return fmt.Errorf("open null device: %w", err)
	}
	defer nullFd.Close()

	daemonArgs := []string{"_daemon"}
	if p := platform.AuthPath(); p != "" {
		daemonArgs = append(daemonArgs, "--auth-path", p)
	}
	if d := platform.DataDir(); d != "" {
		daemonArgs = append(daemonArgs, "--data-dir", d)
	}
	if port > 0 {
		daemonArgs = append(daemonArgs, "--port", fmt.Sprintf("%d", port))
	}
	h, err := parseHost()
	if err != nil {
		return err
	}
	daemonArgs = append(daemonArgs, "--host", h)
	if platform.NoAutoUpgrade() {
		daemonArgs = append(daemonArgs, "--no-auto-upgrade")
	}

	cmd := newDaemonCmd(exe, daemonArgs)
	cmd.Stdin = nullFd
	cmd.Stdout = nullFd
	cmd.Stderr = nullFd
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	if err := a.WritePID(cmd.Process.Pid); err != nil {
		return err
	}

	daemonExited := make(chan error, 1)
	go func() { daemonExited <- cmd.Wait() }()

	ready := false
	deadline := time.Now().Add(readyTimeout)
	lastDot := time.Now()
	dotCount := 0
	for time.Now().Before(deadline) {
		if url, _ := a.ServerURL(); url != "" {
			ready = true
			break
		}
		select {
		case <-daemonExited:
			goto waitDone
		default:
		}
		if time.Since(lastDot) >= 3*time.Second {
			fmt.Print(".")
			dotCount++
			lastDot = time.Now()
		}
		time.Sleep(200 * time.Millisecond)
	}
waitDone:
	if dotCount > 0 {
		fmt.Println()
	}

	if !ready {
		printError("cs-cloud failed to start")
		fmt.Println()
		fmt.Println(headingStyle.Render("Need help?"))
		fmt.Println("  • Check the error message above")
		fmt.Printf("  • Share the logs with the developers: %s\n", valueStyle.Render(filepath.Join(a.RootDir(), "app.log")))
		os.Exit(1)
	}

	url, _ := a.ServerURL()
	printSuccess("started")

	printSection("Developer info")
	printKV("pid", fmt.Sprintf("%d", cmd.Process.Pid))
	printKV("mode", mode)
	if cred, _ := a.Credentials(); cred != nil {
		if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
			user := claims.ResolveDisplayName()
			p := claims.ResolveProvider()
			if p != "" || user != "" {
				printKV("user", p+"/"+user)
			}
		}
	}
	// printKV("url", url)
	printKV("docs", url+"/api/v1/docs")
	printKV("swagger docs", url+"/api/v1/docs")
	printKV("logs", filepath.Join(a.RootDir(), "app.log"))

	if mode == "cloud" {
		webURL := strings.TrimSuffix(a.CloudBaseURL(), "/cloud-api") + "/cloud"
		fmt.Println()
		fmt.Println(headingStyle.Render("→ Cloud dashboard"))
		fmt.Printf("  %s\n", valueStyle.Render(webURL))
		fmt.Println()
		fmt.Println("Login successful. Visit " + webURL + " to use cloud services.")
	}
	return nil
}

func registerWithLogin(ctx context.Context, a *app.App) (*device.DeviceInfo, error) {
	info, err := device.Register(ctx, a.Config())
	if err != nil {
		if device.IsMissingAuthError(err) || device.IsExpiredAuthError(err) || device.IsAuthError(err) {
			printInfo("Cloud registration requires CoStrict login, starting login flow...")
			if _, loginErr := provider.LoginCoStrict(ctx); loginErr != nil {
				return nil, loginErr
			}
			printSuccess("CoStrict login completed")
			info, err = device.Register(ctx, a.Config())
		}
		if device.IsInvalidDeviceTokenError(err) {
			_ = device.ClearDevice()
			if device.IsMissingAuthError(err) || device.IsExpiredAuthError(err) || device.IsAuthError(err) {
				_, _ = provider.LoginCoStrict(ctx)
			}
			info, err = device.Register(ctx, a.Config())
		}
		if err != nil {
			return nil, err
		}
	}
	return info, nil
}

func printRegDebugInfo(a *app.App) {
	printSection("Debug info")
	printKV("cloud_url", a.CloudBaseURL())
	if devInfo, _ := a.Device(); devInfo != nil {
		printKV("device_id", devInfo.DeviceID)
		printKV("legacy_device_id", device.GetLegacyDeviceID())
	} else {
		printKV("device_id", provider.GenerateMachineID())
		printKV("legacy_device_id", device.GetLegacyDeviceID())
	}
}

func checkAndApplyUpdates(a *app.App) error {
	printInfo("Checking for updates...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve exe symlink: %w", err)
	}

	if _, statErr := os.Stat(exe + ".new"); statErr == nil {
		printInfo("Staged update detected, applying...")
		if swapErr := updater.NewManager(a.CloudBaseURL(), a.RootDir()).SwapStagedBinary(); swapErr != nil {
			return fmt.Errorf("swap staged binary: %w", swapErr)
		}
		printSuccess("Update applied successfully. Restarting to use new version...")
		os.Setenv("CS_CLOUD_SKIP_UPDATE_CHECK", "true")
		restartWithNewBinary(exe)
		return nil
	}

	updaterMgr := updater.NewManager(
		a.CloudBaseURL(), a.RootDir(),
		updater.WithPolicy(updater.PolicyAuto),
		updater.WithAutoCheck(false),
	)

	result, err := updaterMgr.CheckNow(ctx)
	if err != nil {
		return fmt.Errorf("check updates: %w", err)
	}

	if !result.CanUpdate {
		printInfo("Already on the latest version: %s", version.Get())
		return nil
	}

	printInfo("Update available: %s → %s", version.Get(), result.Version)
	if result.Changelog != "" {
		printInfo("Changelog: %s", result.Changelog)
	}
	printInfo("Automatically applying update...")

	printInfo("Applying update...")
	onProgress := func(phase string, progress float64, message string) {
		if progress > 0 && int(progress)%20 == 0 {
			fmt.Printf("[%s] %.0f%%: %s\n", phase, progress, message)
		}
	}

	if err := updaterMgr.ApplyWithProgress(ctx, result.Version, onProgress); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	if err := updaterMgr.SwapStagedBinary(); err != nil {
		return fmt.Errorf("swap staged binary: %w", err)
	}

	printSuccess("Update applied successfully. Restarting to use new version...")
	os.Setenv("CS_CLOUD_SKIP_UPDATE_CHECK", "true")
	restartWithNewBinary(exe)
	return nil
}

func restartWithNewBinary(exe string) {
	args := os.Args
	if len(args) > 1 && args[1] == "_daemon" {
		args = []string{"start"}
	}

	cmd := newDaemonCmd(exe, args)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		printError("start updated process: %v", err)
		os.Exit(1)
	}

	os.Exit(0)
}
