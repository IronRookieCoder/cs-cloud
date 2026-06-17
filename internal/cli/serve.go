package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/localserver"
	"cs-cloud/internal/version"
)

func serve(a *app.App) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port, err := parsePort()
	if err != nil {
		return err
	}
	host, err := parseHost()
	if err != nil {
		return err
	}

	srv := localserver.New(localserver.WithVersion(version.Get()), localserver.WithConfig(a.Config()), localserver.WithRootDir(a.RootDir()))

	if err := srv.Manager().InitDefaultAgent(ctx, a.Config().DefaultAgent, a.Config().AgentCommand, a.Config().AgentVersionCommand, a.Config().AgentWorkspace, a.Config().AgentEnv); err != nil {
		return fmt.Errorf("failed to init agent: %w", err)
	}
	printSuccess("Agent started (endpoint=%s)", srv.Manager().Endpoint())

	if err := srv.Start(net.JoinHostPort(host, fmt.Sprintf("%d", port))); err != nil {
		return err
	}
	if err := a.SaveServerURL(srv.URL()); err != nil {
		return err
	}

	printTitle("cs-cloud serve")
	printSuccess("Server running")
	printKV("url", srv.URL())
	printKV("docs", srv.URL()+"/api/v1/docs")

	agents, _ := srv.Manager().DetectAgents(ctx)
	for _, ag := range agents {
		if ag.Available {
			printSuccess("Agent detected: %s (%s)", ag.Name, ag.Backend)
		}
	}

	printInfo("Press Ctrl+C to stop")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer shutdownCancel()

	fmt.Println()
	printInfo("Shutting down...")
	return srv.Shutdown(shutdownCtx)
}
