package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
)

var (
	cachedDeviceID        string
	cachedDeviceIDOnce    sync.Once
	cachedLegacyDeviceID  string
	cachedLegacyOnce      sync.Once
)

func GetDeviceID() string {
	cachedDeviceIDOnce.Do(func() {
		if id := loadStoredDeviceID(); id != "" {
			cachedDeviceID = id
			return
		}
		cachedDeviceID = provider.GenerateMachineID()
	})
	return cachedDeviceID
}

// loadStoredDeviceID reads device_id from device_v2.json or device.json.
func loadStoredDeviceID() string {
	if id := loadIDFromFile(DeviceV2Path); id != "" {
		return id
	}
	return loadIDFromFile(DevicePath)
}

func loadIDFromFile(pathFn func() (string, error)) string {
	p, err := pathFn()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var info DeviceInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return ""
	}
	return info.DeviceID
}

func GetLegacyDeviceID() string {
	cachedLegacyOnce.Do(func() {
		cachedLegacyDeviceID = provider.GenerateOldMachineID()
	})
	return cachedLegacyDeviceID
}

type DeviceInfo struct {
	DeviceID     string `json:"device_id"`
	DeviceToken  string `json:"device_token"`
	AuthUserID   string `json:"auth_user_id"`
	RegisteredAt string `json:"registered_at"`
	BaseURL      string `json:"base_url"`

	LegacyDeviceID string `json:"legacy_device_id,omitempty"`
	MigratedFrom   string `json:"-"`
}

func DevicePath() (string, error) {
	return filepath.Join(platform.CoStrictShareDir(), "device.json"), nil
}

func DeviceV2Path() (string, error) {
	return filepath.Join(platform.CoStrictShareDir(), "device_v2.json"), nil
}

// deviceV2FileExists checks whether device_v2.json exists on disk.
func deviceV2FileExists() bool {
	p, err := DeviceV2Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// loadDeviceFrom reads device info from a file path returned by pathFn.
func loadDeviceFrom(pathFn func() (string, error)) (*DeviceInfo, error) {
	p, err := pathFn()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var info DeviceInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("decode device file: %w", err)
	}
	if info.DeviceToken == "" {
		return nil, nil
	}
	return &info, nil
}

// LoadDevice checks device_v2.json first, then falls back to device.json.
func LoadDevice() (*DeviceInfo, error) {
	info, err := loadDeviceFrom(DeviceV2Path)
	if err != nil {
		return nil, err
	}
	if info != nil {
		return info, nil
	}
	return loadDeviceFrom(DevicePath)
}

// SaveDevice always writes to device_v2.json (the canonical device identity file).
func SaveDevice(info *DeviceInfo) error {
	p, err := DeviceV2Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create device dir: %w", err)
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device file: %w", err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write device file: %w", err)
	}
	return nil
}

// ClearDeviceV2 deletes device_v2.json. Used when the device owner changes.
func ClearDeviceV2() error {
	p, err := DeviceV2Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
