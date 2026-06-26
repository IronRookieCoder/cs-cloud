package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/provider"
)

func login(a *app.App) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cred, err := provider.LoginCoStrict(ctx)
	if err != nil {
		return err
	}

	printTitle("login")
	printSuccess("Login successful")
	if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
		user := claims.ResolveDisplayName()
		p := claims.ResolveProvider()
		if p != "" || user != "" {
			printKV("user", p+"/"+user)
		}
	}
	printKV("machine_id", cred.MachineID)
	printKV("base_url", cred.BaseURL)

	info, err := device.Register(ctx, a.Config())
	if err != nil {
		if device.IsMissingAuthError(err) || device.IsExpiredAuthError(err) {
			printWarn("Authentication issue, please try login again")
			return err
		}
		printWarn("Device registration failed, retrying...")
		_ = device.ClearDevice()
		_ = device.ClearDeviceV2()
		info, err = device.Register(ctx, a.Config())
		if err != nil {
			printRegDebugInfo(a)
			return err
		}
	}
	printSuccess("Device registered")
	if info.MigratedFrom != "" {
		printInfo("Migrated from legacy device %s", info.MigratedFrom)
	}
	printKV("device_id", info.DeviceID)

	printInfo("Checking gateway connectivity...")
	if gwErr := device.CheckGatewayConnectivity(ctx, info); gwErr != nil {
		printError("Gateway connectivity check failed")
		printKV("error", gwErr.Error())
		printKV("hint", "Check your network connection and try again")
		return gwErr
	}
	printSuccess("Gateway connectivity OK")

	if running, _, _ := a.DaemonStatus(); running {
		printInfo("Restarting daemon with new credentials...")
		return restart(a)
	}

	fmt.Println()
	printInfo("Starting cs-cloud...")
	return start(a)
}

func logout(a *app.App) error {
	if err := provider.DeleteCredentials(); err != nil {
		return fmt.Errorf("delete credentials: %w", err)
	}
	if err := device.ClearDevice(); err != nil {
		return fmt.Errorf("clear device: %w", err)
	}
	if err := device.ClearDeviceV2(); err != nil {
		return fmt.Errorf("clear device v2: %w", err)
	}
	printSuccess("Logged out")
	return nil
}
