package cli

import (
	"context"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
)

func register(a *app.App) error {
	ctx := context.Background()
	info, err := device.Register(ctx, a.Config())
	if err != nil {
		return err
	}
	printTitle("cs-cloud register")
	printSuccess("Device registered")
	printKV("device_id", info.DeviceID)
	printKV("legacy_device_id", device.GetLegacyDeviceID())
	printKV("base_url", info.BaseURL)
	return nil
}
