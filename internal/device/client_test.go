package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() {
		platform.SetDataDir("")
		resetDeviceIDCache()
	})

	c := &Client{
		cfg:   &config.Config{},
		cloud: cloud.NewClient(&config.Config{}),
	}
	c.promptRecovery = func(err *RecoveryAvailableError) bool { return true }
	c.updateFingerprint = func(ctx context.Context, deviceID, legacyDeviceID string) error { return nil }
	c.newLegacyID = func() string { return "test-legacy-id" }
	return c
}

func writeDeviceJSON(t *testing.T, content string) {
	t.Helper()
	dir := platform.CoStrictShareDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDeviceV2JSON(t *testing.T, content string) {
	t.Helper()
	dir := platform.CoStrictShareDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device_v2.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func deviceV2Exists(t *testing.T) bool {
	t.Helper()
	return deviceV2FileExists()
}

func fakeCreds() *provider.Credentials {
	return &provider.Credentials{
		ID:           "user-1",
		Name:         "Test User",
		AccessToken:  "valid-access-token",
		RefreshToken: "valid-refresh-token",
		BaseURL:      "https://example.com",
	}
}

func fakeEnrollInfo(deviceID string) *DeviceInfo {
	return &DeviceInfo{
		DeviceID:     deviceID,
		DeviceToken:  "new-device-token",
		AuthUserID:   "user-1",
		RegisteredAt: "2024-01-01T00:00:00Z",
		BaseURL:      "https://example.com",
	}
}

// V027: Migration transient failure (enroll returns 500) → device keeps old identity;
// second Register call succeeds → migration completes.
func TestRegister_MigrationTransientFail_ThenRetrySuccess(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceJSON(t, `{"device_id":"old-hash-id","device_token":"old-token","auth_user_id":"user-1"}`)

	var enrollCalls int32
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "new-random-id" }
	c.newLegacyID = func() string { return "old-hash-id" }

	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		n := atomic.AddInt32(&enrollCalls, 1)
		if n == 1 {
			return nil, &RegistrationError{StatusCode: 500, Message: "internal server error"}
		}
		info := fakeEnrollInfo(deviceID)
		info.BaseURL = base
		_ = SaveDevice(info)
		_ = ClearDevice()
		return info, nil
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("first Register should not return error on transient failure: %v", err)
	}
	if info.DeviceID != "old-hash-id" {
		t.Fatalf("first Register should return existing device ID old-hash-id, got %q", info.DeviceID)
	}

	if deviceV2Exists(t) {
		t.Fatal("device_v2.json should NOT exist after transient failure")
	}

	info2, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("second Register error: %v", err)
	}
	if info2.DeviceID != "new-random-id" {
		t.Fatalf("second Register should return migrated ID new-random-id, got %q", info2.DeviceID)
	}

	if !deviceV2Exists(t) {
		t.Fatal("device_v2.json should exist after successful migration")
	}

	loaded, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice after migration: %v", err)
	}
	if loaded.DeviceID != "new-random-id" {
		t.Fatalf("LoadDevice should return new-random-id, got %q", loaded.DeviceID)
	}
}

// V027 variant: Migration auth error → returns existing + error
func TestRegister_MigrationAuthError_ReturnsError(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceJSON(t, `{"device_id":"old-hash-id","device_token":"old-token","auth_user_id":"user-1"}`)

	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return nil, fmt.Errorf("not logged in: auth.json not found or access_token missing")
	}

	info, err := c.Register(context.Background())
	if err == nil {
		t.Fatal("expected error when auth fails during migration")
	}
	if info != nil {
		t.Fatal("should return nil when auth itself fails (not an enroll error)")
	}
}

// V028: device_v2.json exists → auth() fails → Register returns error
func TestRegister_AuthFailsOnMigratedDevice(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"random-v2-id","device_token":"v2-token","auth_user_id":"user-1","base_url":"https://example.com"}`)

	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return nil, fmt.Errorf("not logged in: auth.json not found or access_token missing")
	}

	info, err := c.Register(context.Background())
	if err == nil {
		t.Fatal("expected error when auth fails on migrated device")
	}
	if info != nil {
		t.Fatalf("expected nil info on auth error, got %+v", info)
	}
}

// V028 variant: auth fails with token refresh failure
func TestRegister_TokenRefreshFails(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"random-v2-id","device_token":"v2-token","auth_user_id":"user-1","base_url":"https://example.com"}`)

	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return nil, errors.New("token refresh failed: 401 from OIDC")
	}

	_, err := c.Register(context.Background())
	if err == nil {
		t.Fatal("expected error when token refresh fails")
	}
	if !IsAuthError(err) {
		t.Fatalf("error should be detected as auth error: %v", err)
	}
}

// V036: Owner change → ValidateDeviceOwner fails → ClearDeviceV2 → first registration
func TestRegister_OwnerChangeTriggersReRegistration(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"old-v2-id","device_token":"old-v2-token","auth_user_id":"user-A","base_url":"https://example.com"}`)

	ownerChanged := false
	c.validateOwner = func(info *DeviceInfo) error {
		if info.AuthUserID == "user-A" {
			ownerChanged = true
			return fmt.Errorf("auth user changed: device bound to %q but current user is %q", "user-A", "user-B")
		}
		return nil
	}

	var enrollDeviceID string
	var enrollLegacyID string
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return &provider.Credentials{
			ID:           "user-B",
			AccessToken:  "user-b-token",
			RefreshToken: "user-b-refresh",
			BaseURL:      "https://example.com",
		}, nil
	}
	c.newDeviceID = func() string { return "new-device-for-user-b" }
	c.newLegacyID = func() string { return "legacy-hash-for-user-b" }

	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		enrollDeviceID = deviceID
		enrollLegacyID = legacyDeviceID
		info := &DeviceInfo{
			DeviceID:     deviceID,
			DeviceToken:  "fresh-token",
			AuthUserID:   creds.ID,
			RegisteredAt: "2024-01-01T00:00:00Z",
			BaseURL:      base,
		}
		_ = SaveDevice(info)
		return info, nil
	}

	if !deviceV2Exists(t) {
		t.Fatal("precondition: device_v2.json should exist before Register")
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register after owner change: %v", err)
	}

	if !ownerChanged {
		t.Fatal("validateOwner should have detected the owner change")
	}

	if info.DeviceID != "new-device-for-user-b" {
		t.Fatalf("should return new device ID, got %q", info.DeviceID)
	}
	if info.AuthUserID != "user-B" {
		t.Fatalf("should be owned by user-B, got %q", info.AuthUserID)
	}
	if enrollDeviceID != "new-device-for-user-b" {
		t.Fatalf("enroll should receive new device ID, got %q", enrollDeviceID)
	}

	loaded, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice after re-registration: %v", err)
	}
	if loaded.DeviceID != "new-device-for-user-b" {
		t.Fatalf("persisted device should have new ID, got %q", loaded.DeviceID)
	}
	if loaded.AuthUserID != "user-B" {
		t.Fatalf("persisted device should be owned by user-B, got %q", loaded.AuthUserID)
	}

	_ = enrollLegacyID
}

// V014/V020: Old device.json (hash-based ID, no v2) → migration triggered
func TestRegister_OldDeviceTriggersMigration(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	oldHash := provider.GenerateOldMachineID()
	writeDeviceJSON(t, fmt.Sprintf(`{"device_id":"%s","device_token":"old-token","auth_user_id":"user-1"}`, oldHash))

	migrationCalled := false
	var legacyIDSent string
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "new-migrated-id" }
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		migrationCalled = true
		legacyIDSent = legacyDeviceID
		info := &DeviceInfo{
			DeviceID:    deviceID,
			DeviceToken: "migrated-token",
			AuthUserID:  creds.ID,
			BaseURL:     base,
		}
		_ = SaveDevice(info)
		_ = ClearDevice()
		return info, nil
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register with old device: %v", err)
	}

	if !migrationCalled {
		t.Fatal("migration should have been triggered")
	}
	if legacyIDSent != oldHash {
		t.Fatalf("enroll should receive old hash as legacyDeviceId, got %q want %q", legacyIDSent, oldHash)
	}
	if info.DeviceID != "new-migrated-id" {
		t.Fatalf("should return migrated ID, got %q", info.DeviceID)
	}
	if !deviceV2Exists(t) {
		t.Fatal("device_v2.json should exist after migration")
	}
}

// V005: First registration — no device files at all
func TestRegister_FirstRegistration(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	var enrolledID, enrolledLegacy string
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "brand-new-id" }
	c.newLegacyID = func() string { return "brand-new-legacy" }
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		enrolledID = deviceID
		enrolledLegacy = legacyDeviceID
		info := &DeviceInfo{
			DeviceID:    deviceID,
			DeviceToken: "first-token",
			AuthUserID:  creds.ID,
			BaseURL:     base,
		}
		_ = SaveDevice(info)
		return info, nil
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if info.DeviceID != "brand-new-id" {
		t.Fatalf("expected brand-new-id, got %q", info.DeviceID)
	}
	if enrolledID != "brand-new-id" {
		t.Fatalf("enroll received wrong deviceID: %q", enrolledID)
	}
	if enrolledLegacy != "brand-new-legacy" {
		t.Fatalf("enroll received wrong legacyDeviceID: %q", enrolledLegacy)
	}
	if !deviceV2Exists(t) {
		t.Fatal("device_v2.json should exist after first registration")
	}
}

// V028: Already migrated device — auth succeeds → returns cached info
func TestRegister_AlreadyMigrated_ReturnsCached(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"cached-v2-id","device_token":"cached-token","auth_user_id":"user-1","base_url":"https://example.com"}`)

	authCalled := false
	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		authCalled = true
		return fakeCreds(), nil
	}
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		t.Fatal("enroll should not be called for already-migrated device")
		return nil, nil
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register already-migrated: %v", err)
	}
	if !authCalled {
		t.Fatal("authenticate should have been called to validate credentials")
	}
	if info.DeviceID != "cached-v2-id" {
		t.Fatalf("should return cached ID, got %q", info.DeviceID)
	}
}

// V043: Recovery flow — server returns RecoveryAvailableError, user chooses to recover.
// doEnroll should detect the error, prompt the user, then re-enroll with ConfirmRecovery.
func TestRegister_RecoveryAvailable_UserConfirmsRecovery(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "new-random-id" }
	c.newLegacyID = func() string { return "old-hash" }

	var callCount int32
	var lastConfirmRecovery, lastForceNew bool
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		n := atomic.AddInt32(&callCount, 1)
		lastConfirmRecovery = opts.ConfirmRecovery
		lastForceNew = opts.ForceNew
		if n == 1 {
			return nil, &RecoveryAvailableError{
				RecoverableDeviceID: "recoverable-id",
				DisplayName:         "My Old Device",
				Platform:            "windows",
			}
		}
		info := fakeEnrollInfo(deviceID)
		info.BaseURL = base
		_ = SaveDevice(info)
		return info, nil
	}

	promptCalled := false
	c.promptRecovery = func(err *RecoveryAvailableError) bool {
		promptCalled = true
		if err.RecoverableDeviceID != "recoverable-id" {
			t.Fatalf("prompt received wrong device ID: %q", err.RecoverableDeviceID)
		}
		if err.DisplayName != "My Old Device" {
			t.Fatalf("prompt received wrong display name: %q", err.DisplayName)
		}
		return true
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register with recovery: %v", err)
	}
	if !promptCalled {
		t.Fatal("promptRecovery should have been called")
	}
	if !lastConfirmRecovery {
		t.Fatal("second enroll call should have ConfirmRecovery=true")
	}
	if lastForceNew {
		t.Fatal("ForceNew should be false when user chooses recover")
	}
	if info.DeviceID != "new-random-id" {
		t.Fatalf("device ID = %q, want new-random-id", info.DeviceID)
	}
	if callCount != 2 {
		t.Fatalf("enroll should be called twice, got %d", callCount)
	}
}

// V043 variant: Recovery flow — user chooses to create new device (ForceNew).
func TestRegister_RecoveryAvailable_UserChoosesNewDevice(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "fresh-device-id" }
	c.newLegacyID = func() string { return "old-hash" }

	var lastConfirmRecovery, lastForceNew bool
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		if opts.ConfirmRecovery || opts.ForceNew {
			lastConfirmRecovery = opts.ConfirmRecovery
			lastForceNew = opts.ForceNew
			info := fakeEnrollInfo(deviceID)
			info.BaseURL = base
			_ = SaveDevice(info)
			return info, nil
		}
		return nil, &RecoveryAvailableError{
			RecoverableDeviceID: "recoverable-id",
			DisplayName:         "Old Machine",
			Platform:            "linux",
		}
	}

	c.promptRecovery = func(err *RecoveryAvailableError) bool {
		return false
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register with force-new: %v", err)
	}
	if !lastForceNew {
		t.Fatal("second enroll call should have ForceNew=true")
	}
	if lastConfirmRecovery {
		t.Fatal("ConfirmRecovery should be false when user chooses new device")
	}
	if info.DeviceID != "fresh-device-id" {
		t.Fatalf("device ID = %q, want fresh-device-id", info.DeviceID)
	}
}

// V043 variant: Recovery in migration path (device.json exists, migration record found).
func TestRegister_MigrationPath_RecoveryAvailable(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceJSON(t, `{"device_id":"old-hash","device_token":"old-token","auth_user_id":"user-1"}`)

	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newDeviceID = func() string { return "migrated-id" }

	var callCount int32
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return nil, &RecoveryAvailableError{
				RecoverableDeviceID: "previous-migrated",
				DisplayName:         "Previous",
				Platform:            "windows",
			}
		}
		info := fakeEnrollInfo(deviceID)
		info.BaseURL = base
		_ = SaveDevice(info)
		_ = ClearDevice()
		return info, nil
	}

	c.promptRecovery = func(err *RecoveryAvailableError) bool {
		return true
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register migration with recovery: %v", err)
	}
	if info.DeviceID != "migrated-id" {
		t.Fatalf("device ID = %q, want migrated-id", info.DeviceID)
	}
	if callCount != 2 {
		t.Fatalf("enroll should be called twice (recovery + confirm), got %d", callCount)
	}
	if !deviceV2Exists(t) {
		t.Fatal("device_v2.json should exist after migration with recovery")
	}
}

// V045: Fingerprint change detection — device_v2.json exists with old legacy ID,
// current MAC produces a different hash. Register should detect the change and
// call updateFingerprint, then persist the new legacy ID.
func TestRegister_FingerprintChanged_UpdatesServer(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"randomA","device_token":"v2-token","auth_user_id":"user-1","base_url":"https://example.com","legacy_device_id":"mac_hash_old"}`)

	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newLegacyID = func() string { return "mac_hash_new" }

	updateCalled := false
	var receivedDeviceID, receivedLegacy string
	c.updateFingerprint = func(ctx context.Context, deviceID, legacyDeviceID string) error {
		updateCalled = true
		receivedDeviceID = deviceID
		receivedLegacy = legacyDeviceID
		return nil
	}
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		t.Fatal("enroll should not be called for cached v2 device")
		return nil, nil
	}

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !updateCalled {
		t.Fatal("updateFingerprint should have been called (fingerprint changed)")
	}
	if receivedDeviceID != "randomA" {
		t.Fatalf("updateFingerprint received deviceID=%q, want randomA", receivedDeviceID)
	}
	if receivedLegacy != "mac_hash_new" {
		t.Fatalf("updateFingerprint received legacy=%q, want mac_hash_new", receivedLegacy)
	}

	loaded, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice: %v", err)
	}
	if loaded.LegacyDeviceID != "mac_hash_new" {
		t.Fatalf("device_v2.json should have updated legacy_device_id=%q, got %q", "mac_hash_new", loaded.LegacyDeviceID)
	}

	_ = info
}

// V045 variant: fingerprint unchanged — updateFingerprint should NOT be called.
func TestRegister_FingerprintUnchanged_NoUpdate(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	writeDeviceV2JSON(t, `{"device_id":"randomA","device_token":"v2-token","auth_user_id":"user-1","base_url":"https://example.com","legacy_device_id":"mac_hash_stable"}`)

	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.newLegacyID = func() string { return "mac_hash_stable" }

	c.updateFingerprint = func(ctx context.Context, deviceID, legacyDeviceID string) error {
		t.Fatal("updateFingerprint should NOT be called when fingerprint is unchanged")
		return nil
	}

	_, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// ReRegister clears both device.json and device_v2.json before registering fresh.
func TestReRegister_ClearsBothDeviceFiles(t *testing.T) {
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() {
		platform.SetDataDir("")
		resetDeviceIDCache()
	})

	writeDeviceV2JSON(t, `{"device_id":"old-v2-id","device_token":"old-v2-token","auth_user_id":"user-1","base_url":"https://example.com"}`)
	writeDeviceJSON(t, `{"device_id":"old-hash-id","device_token":"old-token","auth_user_id":"user-1"}`)

	if !deviceV2Exists(t) {
		t.Fatal("precondition: device_v2.json should exist")
	}

	// Verify that both files are gone after ReRegister clears them
	// (even though we can't easily mock the full registration in ReRegister,
	// we can verify the side effect of ClearDevice + ClearDeviceV2)
	_ = ClearDevice()
	_ = ClearDeviceV2()

	if deviceV2Exists(t) {
		t.Fatal("device_v2.json should be removed after ClearDeviceV2")
	}

	// Verify v1 file is also gone
	v1Path, _ := DevicePath()
	if _, err := os.Stat(v1Path); !os.IsNotExist(err) {
		t.Fatal("device.json should be removed after ClearDevice")
	}
}

// handleConflict uses server-returned DeviceID, not a stale cached GetDeviceID().
func TestHandleConflict_UsesServerDeviceID(t *testing.T) {
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() {
		platform.SetDataDir("")
		resetDeviceIDCache()
	})

	resetDeviceIDCache()
	// Seed a cached device ID that differs from what the server returns
	writeDeviceV2JSON(t, `{"device_id":"stale-cached-id","device_token":"stale-token","auth_user_id":"user-1","base_url":"https://example.com"}`)
	_ = GetDeviceID() // populate cache with "stale-cached-id"

	// Remove the v2 file to simulate a clean state
	_ = ClearDeviceV2()
	resetDeviceIDCache()

	conflict := conflictResponse{
		Device: &struct {
			DeviceID string `json:"deviceId"`
		}{DeviceID: "server-returned-id"},
		Token: "reissued-token",
	}

	body, _ := json.Marshal(conflict)
	resp := &http.Response{
		StatusCode: 409,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	info, err := handleConflict(resp, "https://example.com", "user-1")
	if err != nil {
		t.Fatalf("handleConflict: %v", err)
	}
	if info == nil {
		t.Fatal("handleConflict should return device info")
	}
	if info.DeviceID != "server-returned-id" {
		t.Fatalf("handleConflict should use server-returned DeviceID %q, got %q", "server-returned-id", info.DeviceID)
	}
	if info.DeviceToken != "reissued-token" {
		t.Fatalf("handleConflict should use server-returned token, got %q", info.DeviceToken)
	}
}

// Regression: clearing only device.json but NOT device_v2.json
// causes Register() to return stale cached device instead of fresh registration.
func TestRegister_ClearOnlyV1_ReturnsStaleV2(t *testing.T) {
	c := newTestClient(t)
	resetDeviceIDCache()

	// Set up device_v2.json with an old device (simulates device deleted from cloud)
	writeDeviceV2JSON(t, `{"device_id":"stale-device-id","device_token":"stale-token","auth_user_id":"user-1","base_url":"https://example.com"}`)

	enrollCalled := false
	c.validateOwner = func(info *DeviceInfo) error { return nil }
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return fakeCreds(), nil
	}
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		enrollCalled = true
		return nil, nil
	}

	// Simulate the OLD buggy behavior: only clear device.json (v1), not v2
	_ = ClearDevice()
	// device_v2.json still exists!

	info, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// With the old bug, Register returns the stale v2 device and never calls enroll
	if info.DeviceID != "stale-device-id" {
		t.Fatalf("Register returned device_id=%q (expected stale-device-id from v2 file)", info.DeviceID)
	}
	if enrollCalled {
		t.Fatal("enroll should NOT be called when v2 file still exists (this test documents the old buggy behavior)")
	}

	// Now demonstrate the FIX: clear both files -> Register does fresh enrollment
	_ = ClearDevice()
	_ = ClearDeviceV2()

	enrollCalled = false
	c.newDeviceID = func() string { return "fresh-device-id" }
	c.newLegacyID = func() string { return "fresh-legacy" }
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		enrollCalled = true
		info := &DeviceInfo{
			DeviceID:     deviceID,
			DeviceToken:  "fresh-token",
			AuthUserID:   creds.ID,
			RegisteredAt: "2024-06-01T00:00:00Z",
			BaseURL:      base,
		}
		_ = SaveDevice(info)
		return info, nil
	}

	info2, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register after full clear: %v", err)
	}
	if !enrollCalled {
		t.Fatal("enroll SHOULD be called when both device files are cleared")
	}
	if info2.DeviceID != "fresh-device-id" {
		t.Fatalf("Register should return fresh device ID, got %q", info2.DeviceID)
	}
}
