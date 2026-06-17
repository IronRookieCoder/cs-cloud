package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/cloud"
	"cs-cloud/internal/device"
	"cs-cloud/internal/localserver"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/tunnel"
	"cs-cloud/internal/updater"
	"cs-cloud/internal/version"
)


func collectRecent(dirs []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(filepath.Clean(dir))
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out
}

type tunnelStatusAdapter struct {
	mgr *tunnel.Manager
}

func (a *tunnelStatusAdapter) TunnelStatus() localserver.TunnelStatus {
	s := a.mgr.Status()
	return localserver.TunnelStatus{
		Connected:   s.Connected,
		ConnectedAt: s.ConnectedAt,
	}
}

func runDaemon(a *app.App) error {
	configureDaemonSignals()

	logger.Init(logger.Config{
		Dir:        a.RootDir(),
		MaxSizeMB:  100,
		MaxAgeDays: 7,
		MaxBackups: 10,
		Console:    false,
	})
	defer logger.Sync()

	logger.Info("[debug] daemon process started (pid=%d)", os.Getpid())

	mode := a.LoadMode()
	a.SaveArgs(os.Args[1:])
	port, err := parsePort()
	if err != nil {
		logger.Error("invalid port: %v", err)
		return err
	}
	host, err := parseHost()
	if err != nil {
		logger.Error("invalid host: %v", err)
		return err
	}

	if err := a.WritePID(os.Getpid()); err != nil {
		logger.Warn("failed to write pid: %v", err)
	}

	logger.Info("[debug] initializing local server...")
	srv := localserver.New(localserver.WithVersion(version.Get()), localserver.WithConfig(a.Config()), localserver.WithRootDir(a.RootDir()))

	ctx := context.Background()
	agentType := a.Config().DefaultAgent
	agentCommand := a.Config().AgentCommand
	logger.Info("[debug] detecting agent (type=%s, command=%q)...", agentType, agentCommand)
	if err := srv.Manager().InitDefaultAgent(ctx, agentType, agentCommand, a.Config().AgentVersionCommand, a.Config().AgentWorkspace, a.Config().AgentEnv); err != nil {
		logger.Error("failed to init agent: %v", err)
		logger.Error("please check your agent_command configuration works correctly in your terminal")
		return err
	}
	logger.Info("agent started (endpoint=%s)", srv.Manager().Endpoint())

	logger.Info("[debug] agent init done, starting HTTP server...")

	if pid := srv.Manager().AgentPID(); pid > 0 {
		if err := a.WriteAgentPID(pid); err != nil {
			logger.Warn("failed to save agent pid: %v", err)
		}
	}

	if err := srv.Start(net.JoinHostPort(host, fmt.Sprintf("%d", port))); err != nil {
		logger.Error("failed to start server: %v", err)
		return err
	}
	logger.Info("[debug] HTTP server started, saving state...")
	if err := a.SaveServerURL(srv.URL()); err != nil {
		logger.Error("failed to save server url: %v", err)
		return err
	}
	if err := a.SaveState("running"); err != nil {
		logger.Error("failed to save state: %v", err)
		return err
	}

	logger.Info("daemon started (version: %s, mode: %s, host: %s, port: %d, auto_upgrade: %v)", version.FullString(), mode, host, srv.Port(), a.Config().AutoUpgrade)
	logger.Info("swagger docs: %s/api/v1/docs", srv.URL())
	recent, err := a.LoadRecentWorkspaces()
	if err != nil {
		logger.Warn("failed to load recent workspaces: %v", err)
	}
	dir, cwdErr := os.Getwd()
	if cwdErr != nil {
		logger.Warn("failed to resolve prewarm workspace: %v", cwdErr)
	} else {
		recent = append([]string{dir}, recent...)
	}
	recent = collectRecent(recent)
	for _, d := range recent {
		srv.TriggerPrewarmIfNeeded(d)
	}

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			srv.TerminalManager().CleanupIdle(30 * time.Minute)
		}
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	if mode == "cloud" {
		info, err := device.LoadDevice()
		if err != nil || info == nil {
			logger.Error("device not registered")
			return nil
		}

		if ownerErr := device.ValidateDeviceOwner(info); ownerErr != nil {
			logger.Warn("[daemon] %v, attempting re-registration...", ownerErr)
			info, err = device.ReRegister(context.Background(), a.Config())
			if err != nil {
				logger.Error("[daemon] re-register failed: %v", err)
				return nil
			}
			logger.Info("[daemon] device re-registered successfully (device_id=%s)", info.DeviceID)
		}

		cloudCtx, cloudCancel := context.WithCancel(context.Background())
		defer cloudCancel()

		reporter := localserver.NewCommandReporter()
		dispatcher := localserver.NewCommandDispatcher(a, reporter)
		srv.SetDispatcher(dispatcher)

		deviceClient := device.NewClient(a.Config())
		dispatcher.BindDeviceClient(deviceClient)
		// Start notify forwarder for interactive notification loop
		cloudClient := cloud.NewClient(a.Config())
		var credBaseURL string
		if cred, err := provider.LoadCredentials(); err == nil && cred != nil {
			credBaseURL = cred.BaseURL
		}
		notifyForwarder := cloud.NewNotifyForwarder(srv.EventBus(), cloudClient, info.DeviceID, info.DeviceToken, credBaseURL, a.Config().NotifyBufferSeconds, a.Config().PermissionBufferSeconds)
		if err := notifyForwarder.Validate(); err != nil {
			logger.Warn("[daemon] notify forwarder not started: %v", err)
		} else {
			notifyForwarder.Start(cloudCtx)
			logger.Info("[daemon] notify forwarder started deviceID=%s credBaseURL=%s", info.DeviceID, credBaseURL)
		}

		updaterMgr := updater.NewManager(
			a.CloudBaseURL(), a.RootDir(),
			updater.WithPolicy(updater.PolicyAuto),
			updater.WithAutoCheck(a.Config().AutoUpgrade),
		)
		dispatcher.BindUpdater(updaterMgr)
		srv.SetUpdateChecker(updater.NewChecker(a.CloudBaseURL()))
		go updaterMgr.Run(cloudCtx)

		if updaterMgr.DidVerifyOnStartup() {
			dispatcher.MarkUpgradeVerified()
			logger.Info("[daemon] upgrade verified on startup, enabling grace period for stale commands")
		}

		tunnelMgr := tunnel.NewManager()
		dispatcher.BindTunnel(tunnelMgr)
		srv.SetTunnelStatusProvider(&tunnelStatusAdapter{mgr: tunnelMgr})

		restarter := func() {
			logger.Info("[daemon] self-restart triggered")

			logger.Info("[daemon] stopping tunnel gracefully...")
			tunnelMgr.Stop()
			logger.Info("[daemon] tunnel stopped")

			logger.Info("[daemon] cancelling cloud context...")
			cloudCancel()

			logger.Info("[daemon] stopping agent and terminals...")
			srv.Manager().KillAll()
			srv.TerminalManager().CloseAll()

			app.SelfRestart(a)
		}
		dispatcher.BindRestarter(restarter)
		dispatcher.BindAgentRestarter(func(ctx context.Context, onProgress func(phase string, progress float64, message string)) error {
			return srv.Manager().RestartDefaultAgent(ctx, agentType, agentCommand, a.Config().AgentVersionCommand, a.Config().AgentWorkspace, a.Config().AgentEnv, onProgress)
		})

		triggerHeartbeat := device.HeartbeatLoop(cloudCtx, a.Config(), tunnelMgr.IsConnected, func(cmds []device.CloudCommand) {
			dispatcher.HandleHeartbeatCommands(cmds)
		})
		tunnelMgr.SetSendHeartbeat(triggerHeartbeat)

		go tunnel.RunManagedTunnel(cloudCtx, srv.Port(), tunnelMgr, a.Config())

		if runtime.GOOS == "windows" {
			a.RemoveStopFile()
			go func() {
				for {
					time.Sleep(500 * time.Millisecond)
					if a.StopFileExists() {
						shutdown <- syscall.SIGTERM
						return
					}
				}
			}()
		}

		select {
		case <-updaterMgr.RestartCh:
			logger.Info("[daemon] upgrade completed, initiating self-restart")
			restarter()
		case <-shutdown:
			logger.Info("daemon shutting down")
		}
	} else {
		if runtime.GOOS == "windows" {
			a.RemoveStopFile()
			go func() {
				for {
					time.Sleep(500 * time.Millisecond)
					if a.StopFileExists() {
						shutdown <- syscall.SIGTERM
						return
					}
				}
			}()
		}

		<-shutdown
		logger.Info("daemon shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	a.SaveState("stopped")
	a.SaveServerURL("")
	a.RemovePID()
	a.RemoveAgentPID()

	logger.Info("daemon stopped")
	logger.Sync()
	os.Exit(0)
	return nil
}
