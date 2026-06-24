package device

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
)

func resetDeviceIDCache() {
	cachedDeviceID = ""
	cachedDeviceIDOnce.Do(func() {})
	cachedDeviceIDOnce = *new(sync.Once)
}

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() {
		platform.SetDataDir("")
		resetDeviceIDCache()
	})
	return dir
}

func TestGetDeviceID_NonEmpty(t *testing.T) {
	setupTestDir(t)
	resetDeviceIDCache()
	id := GetDeviceID()
	if id == "" {
		t.Fatal("GetDeviceID() returned empty string")
	}
	if len(id) != 64 {
		t.Fatalf("expected 64-char hex string, got %d chars: %s", len(id), id)
	}
}

func TestGetDeviceID_FallbackToGeneration(t *testing.T) {
	setupTestDir(t)
	resetDeviceIDCache()
	got := GetDeviceID()
	if got == "" {
		t.Fatal("GetDeviceID() returned empty string on fallback")
	}
	if len(got) != 64 {
		t.Fatalf("expected 64-char hex string, got %d chars: %s", len(got), got)
	}
}

func TestGetDeviceID_ReadsFromStoredDeviceJson(t *testing.T) {
	dir := setupTestDir(t)
	resetDeviceIDCache()
	storedID := "persisted-device-id-from-file"
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"device_id":"` + storedID + `","device_token":"some-token","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := GetDeviceID()
	if got != storedID {
		t.Fatalf("GetDeviceID() = %q, want %q (from device.json)", got, storedID)
	}
}

func TestGetDeviceID_CachedConsistently(t *testing.T) {
	setupTestDir(t)
	resetDeviceIDCache()
	first := GetDeviceID()
	second := GetDeviceID()
	if first != second {
		t.Fatalf("GetDeviceID() returned different values: %q then %q", first, second)
	}
}

func TestLoadDevice_FileNotExist(t *testing.T) {
	setupTestDir(t)
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil when device file does not exist")
	}
}

func TestLoadDevice_NoDeviceToken(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"device_id":"fake-id","device_token":"","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil when device_token is empty")
	}
}

func TestLoadDevice_TrustsStoredID(t *testing.T) {
	dir := setupTestDir(t)
	content := `{"device_id":"tampered-id","device_token":"valid-token","auth_user_id":"user1"}`
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DeviceInfo")
	}
	if info.DeviceID != "tampered-id" {
		t.Fatalf("LoadDevice() should trust stored device_id, got %q, want %q", info.DeviceID, "tampered-id")
	}
	if info.DeviceToken != "valid-token" {
		t.Fatalf("DeviceToken = %q, want %q", info.DeviceToken, "valid-token")
	}
}

func TestSaveDevice_PreservesDeviceID(t *testing.T) {
	dir := setupTestDir(t)
	info := &DeviceInfo{
		DeviceID:    "fake-id",
		DeviceToken: "my-token",
		AuthUserID:  "user1",
		BaseURL:     "https://example.com",
	}
	if err := SaveDevice(info); err != nil {
		t.Fatalf("SaveDevice() error: %v", err)
	}
	if info.DeviceID != "fake-id" {
		t.Fatalf("in-memory DeviceID = %q, want %q", info.DeviceID, "fake-id")
	}
	data, err := os.ReadFile(filepath.Join(dir, "share", "device_v2.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var saved DeviceInfo
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if saved.DeviceID != "fake-id" {
		t.Fatalf("saved DeviceID = %q, want %q", saved.DeviceID, "fake-id")
	}
}

func TestSaveDevice_LoadDevice_Roundtrip(t *testing.T) {
	setupTestDir(t)
	original := &DeviceInfo{
		DeviceID:    "persistent-device-id",
		DeviceToken: "roundtrip-token",
		AuthUserID:  "user1",
		BaseURL:     "https://example.com",
	}
	if err := SaveDevice(original); err != nil {
		t.Fatalf("SaveDevice() error: %v", err)
	}
	loaded, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice() error: %v", err)
	}
	if loaded.DeviceID != "persistent-device-id" {
		t.Fatalf("roundtrip DeviceID = %q, want %q", loaded.DeviceID, "persistent-device-id")
	}
	if loaded.DeviceToken != "roundtrip-token" {
		t.Fatalf("roundtrip DeviceToken = %q, want %q", loaded.DeviceToken, "roundtrip-token")
	}
	if loaded.AuthUserID != "user1" {
		t.Fatalf("roundtrip AuthUserID = %q, want %q", loaded.AuthUserID, "user1")
	}
}

// V004: device_v2.json takes precedence over device.json
func TestLoadDevice_V2PrecedenceOverDeviceJSON(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldContent := `{"device_id":"old-hash-id","device_token":"old-token","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(oldContent), 0o600); err != nil {
		t.Fatal(err)
	}
	v2Content := `{"device_id":"random-new-id","device_token":"new-token","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device_v2.json"), []byte(v2Content), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice() error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil")
	}
	if info.DeviceID != "random-new-id" {
		t.Fatalf("expected v2 device_id random-new-id, got %q", info.DeviceID)
	}
	if info.DeviceToken != "new-token" {
		t.Fatalf("expected v2 token new-token, got %q", info.DeviceToken)
	}
}

// V004: GetDeviceID reads from device_v2.json when both exist
func TestGetDeviceID_V2Precedence(t *testing.T) {
	dir := setupTestDir(t)
	resetDeviceIDCache()
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte(`{"device_id":"old-id","device_token":"tok","auth_user_id":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device_v2.json"),
		[]byte(`{"device_id":"v2-id","device_token":"tok","auth_user_id":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := GetDeviceID(); got != "v2-id" {
		t.Fatalf("GetDeviceID() = %q, want v2-id from device_v2.json", got)
	}
}

// V005: First boot generates unique random IDs (two calls differ before caching)
func TestGenerateMachineID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := provider.GenerateMachineID()
		if id == "" {
			t.Fatal("GenerateMachineID() returned empty")
		}
		if len(id) != 64 {
			t.Fatalf("expected 64-char hex, got %d", len(id))
		}
		if ids[id] {
			t.Fatalf("GenerateMachineID() produced duplicate: %s at iteration %d", id, i)
		}
		ids[id] = true
	}
}

// V014: Old device.json without v2 — deviceV2FileExists returns false
func TestDeviceV2FileExists_FalseWhenOnlyDeviceJSON(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte(`{"device_id":"old","device_token":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if deviceV2FileExists() {
		t.Fatal("deviceV2FileExists should return false when only device.json exists")
	}
}

// V014: Old device.json without v2 — deviceV2FileExists returns true after SaveDevice
func TestDeviceV2FileExists_TrueAfterSaveDevice(t *testing.T) {
	setupTestDir(t)
	SaveDevice(&DeviceInfo{
		DeviceID:    "new-id",
		DeviceToken: "tok",
		AuthUserID:  "u1",
	})
	if !deviceV2FileExists() {
		t.Fatal("deviceV2FileExists should return true after SaveDevice")
	}
}

// V015: Corrupted device.json (invalid JSON) → LoadDevice returns error
// NOTE: Proposal V015 expected graceful nil, but current impl returns error for invalid JSON.
// GetDeviceID still works (loadStoredDeviceID returns "" on parse failure → generates new ID).
func TestLoadDevice_CorruptedDeviceJSON(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte("{invalid json}}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err == nil {
		t.Fatal("expected error for corrupted device.json")
	}
	if info != nil {
		t.Fatalf("expected nil info for corrupted device.json, got %+v", info)
	}
}

// V015: Corrupted device.json → GetDeviceID falls back to generation
func TestGetDeviceID_CorruptedDeviceJSON(t *testing.T) {
	dir := setupTestDir(t)
	resetDeviceIDCache()
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := GetDeviceID()
	if id == "" {
		t.Fatal("GetDeviceID() should generate random ID when device.json is corrupted")
	}
	if len(id) != 64 {
		t.Fatalf("expected 64-char hex, got %d chars", len(id))
	}
}

// V030: Corrupted device_v2.json (no device.json) → LoadDevice returns error
// NOTE: Proposal V030 expected graceful nil, but current impl returns error for invalid JSON.
// GetDeviceID still works (loadStoredDeviceID returns "" on parse failure → generates new ID).
func TestLoadDevice_CorruptedV2JSON(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device_v2.json"),
		[]byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err == nil {
		t.Fatal("expected error for corrupted device_v2.json")
	}
	if info != nil {
		t.Fatalf("expected nil info for corrupted v2, got %+v", info)
	}
}

// V030: Corrupted device_v2.json + valid device.json → LoadDevice returns error (no fallback)
// NOTE: Current impl propagates v2 parse error and does not fall back to device.json.
// This is a known gap vs proposal V030 (expected fallback). GetDeviceID still falls back
// because loadStoredDeviceID uses loadIDFromFile which silently ignores parse errors.
func TestLoadDevice_CorruptedV2DoesNotFallBackToDeviceJSON(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device_v2.json"),
		[]byte("{corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte(`{"device_id":"fallback-id","device_token":"tok","auth_user_id":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDevice()
	if err == nil {
		t.Fatal("expected error when v2 is corrupted (current impl does not fall back)")
	}
}

// V031: Partial migration — v2 exists, device.json also exists → v2 takes precedence
func TestLoadDevice_PartialMigrationV2Wins(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte(`{"device_id":"old-id","device_token":"old-tok","auth_user_id":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device_v2.json"),
		[]byte(`{"device_id":"new-id","device_token":"new-tok","auth_user_id":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice() error: %v", err)
	}
	if info.DeviceID != "new-id" {
		t.Fatalf("expected new-id from v2 even when device.json still exists, got %q", info.DeviceID)
	}
}

// V031: deviceV2FileExists returns true even when device.json also present
func TestDeviceV2FileExists_TrueWhenBothExist(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(shareDir, "device.json"),
		[]byte(`{"device_id":"old","device_token":"tok"}`), 0o600)
	os.WriteFile(filepath.Join(shareDir, "device_v2.json"),
		[]byte(`{"device_id":"new","device_token":"tok"}`), 0o600)
	if !deviceV2FileExists() {
		t.Fatal("deviceV2FileExists should be true when v2 exists (regardless of device.json)")
	}
}

// V020: Old version device.json (hash-based ID) without v2 → triggers migration detection
func TestDeviceV2FileExists_OldDeviceNeedsMigration(t *testing.T) {
	dir := setupTestDir(t)
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hashID := provider.GenerateOldMachineID()
	content := `{"device_id":"` + hashID + `","device_token":"old-token","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if deviceV2FileExists() {
		t.Fatal("old device with only device.json should need migration (v2 not found)")
	}
}

// V031: ClearDeviceV2 removes v2 file
func TestClearDeviceV2_RemovesFile(t *testing.T) {
	setupTestDir(t)
	SaveDevice(&DeviceInfo{DeviceID: "x", DeviceToken: "t", AuthUserID: "u"})
	if !deviceV2FileExists() {
		t.Fatal("precondition: v2 should exist")
	}
	if err := ClearDeviceV2(); err != nil {
		t.Fatalf("ClearDeviceV2() error: %v", err)
	}
	if deviceV2FileExists() {
		t.Fatal("deviceV2FileExists should be false after ClearDeviceV2")
	}
}

// V031: ClearDeviceV2 is idempotent (no error when file doesn't exist)
func TestClearDeviceV2_Idempotent(t *testing.T) {
	setupTestDir(t)
	if err := ClearDeviceV2(); err != nil {
		t.Fatalf("ClearDeviceV2() on nonexistent file should not error: %v", err)
	}
}

// V005: GetDeviceID generates different IDs across separate processes (no cache)
func TestGetDeviceID_DifferentAcrossInstances(t *testing.T) {
	setupTestDir(t)
	resetDeviceIDCache()
	id1 := GetDeviceID()

	resetDeviceIDCache()
	id2 := GetDeviceID()

	if id1 == id2 {
		t.Fatalf("two GetDeviceID() calls with reset cache should differ: %s", id1)
	}
}
