package device

import (
	"context"
	"errors"
	"testing"
)

func TestPrepareDaemonDeviceRequiresCompleteRegistration(t *testing.T) {
	_, err := prepareDaemonDevice(context.Background(), daemonDeviceDependencies{
		load: func() (*DeviceInfo, error) { return nil, nil },
	})
	if !errors.Is(err, ErrRegistrationRequired) {
		t.Fatalf("error = %v, want ErrRegistrationRequired", err)
	}
}

func TestPrepareDaemonDeviceReRegistersWhenOwnerChanged(t *testing.T) {
	stale := &DeviceInfo{DeviceID: "stale", DeviceToken: "stale-token"}
	fresh := &DeviceInfo{DeviceID: "fresh", DeviceToken: "fresh-token"}
	reRegisterCalls := 0

	got, err := prepareDaemonDevice(context.Background(), daemonDeviceDependencies{
		load:          func() (*DeviceInfo, error) { return stale, nil },
		validateOwner: func(*DeviceInfo) error { return errors.New("owner changed") },
		reRegister: func(context.Context) (*DeviceInfo, error) {
			reRegisterCalls++
			return fresh, nil
		},
	})
	if err != nil {
		t.Fatalf("prepareDaemonDevice: %v", err)
	}
	if got != fresh || reRegisterCalls != 1 {
		t.Fatalf("device = %#v, re-register calls = %d", got, reRegisterCalls)
	}
}
