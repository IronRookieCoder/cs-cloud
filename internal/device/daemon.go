package device

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cs-cloud/internal/config"
)

var ErrRegistrationRequired = errors.New("device registration is required")

type daemonDeviceDependencies struct {
	load          func() (*DeviceInfo, error)
	validateOwner func(*DeviceInfo) error
	reRegister    func(context.Context) (*DeviceInfo, error)
}

func PrepareDaemonDevice(ctx context.Context, cfg *config.Config) (*DeviceInfo, error) {
	return prepareDaemonDevice(ctx, daemonDeviceDependencies{
		load:          LoadDevice,
		validateOwner: ValidateDeviceOwner,
		reRegister: func(ctx context.Context) (*DeviceInfo, error) {
			return ReRegister(ctx, cfg)
		},
	})
}

func prepareDaemonDevice(ctx context.Context, deps daemonDeviceDependencies) (*DeviceInfo, error) {
	info, err := deps.load()
	if err != nil {
		return nil, fmt.Errorf("load device registration: %w", err)
	}
	if !completeDeviceRegistration(info) {
		return nil, ErrRegistrationRequired
	}
	if err := deps.validateOwner(info); err == nil {
		return info, nil
	}
	info, err = deps.reRegister(ctx)
	if err != nil {
		return nil, fmt.Errorf("re-register device: %w", err)
	}
	if !completeDeviceRegistration(info) {
		return nil, ErrRegistrationRequired
	}
	return info, nil
}

func completeDeviceRegistration(info *DeviceInfo) bool {
	return info != nil && strings.TrimSpace(info.DeviceID) != "" && strings.TrimSpace(info.DeviceToken) != ""
}
