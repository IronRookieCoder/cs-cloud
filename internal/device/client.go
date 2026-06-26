package device

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
)

type enrollOptions struct {
	ConfirmRecovery bool
	ForceNew        bool
}

type RecoveryAvailableError struct {
	RecoverableDeviceID string
	DisplayName         string
	Platform            string
	LastConnectedAt     string
}

func (e *RecoveryAvailableError) Error() string {
	return fmt.Sprintf("device recovery available: %s (%s)", e.DisplayName, e.RecoverableDeviceID)
}

type Client struct {
	cfg   *config.Config
	cloud *cloud.Client

	authenticate     func(ctx context.Context) (*provider.Credentials, error)
	enrollDevice     func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error)
	validateOwner    func(info *DeviceInfo) error
	newDeviceID      func() string
	newLegacyID      func() string
	promptRecovery   func(err *RecoveryAvailableError) bool
	updateFingerprint func(ctx context.Context, deviceID, legacyDeviceID string) error
}

func NewClient(cfg *config.Config) *Client {
	c := &Client{cfg: cfg, cloud: cloud.NewClient(cfg)}
	c.authenticate = func(ctx context.Context) (*provider.Credentials, error) {
		return auth(ctx, c.cloud)
	}
	c.enrollDevice = func(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
		return enroll(ctx, c.cloud, creds, base, deviceID, legacyDeviceID, opts)
	}
	c.validateOwner = ValidateDeviceOwner
	c.newDeviceID = provider.GenerateMachineID
	c.newLegacyID = provider.GenerateOldMachineID
	c.promptRecovery = defaultPromptRecovery
	c.updateFingerprint = func(ctx context.Context, deviceID, legacyDeviceID string) error {
		return updateFingerprint(ctx, c.cloud, deviceID, legacyDeviceID)
	}
	return c
}

func (c *Client) CloudBaseURL() string {
	cred, err := provider.LoadCredentials()
	if err != nil || cred == nil {
		return c.cloud.CloudBaseURL("")
	}
	return c.cloud.CloudBaseURL(cred.BaseURL)
}

func (c *Client) doEnroll(ctx context.Context, creds *provider.Credentials, base, deviceID, legacyDeviceID string) (*DeviceInfo, error) {
	info, err := c.enrollDevice(ctx, creds, base, deviceID, legacyDeviceID, enrollOptions{})
	if err != nil {
		var rae *RecoveryAvailableError
		if errors.As(err, &rae) {
			if c.promptRecovery(rae) {
				return c.enrollDevice(ctx, creds, base, deviceID, legacyDeviceID, enrollOptions{ConfirmRecovery: true})
			}
			return c.enrollDevice(ctx, creds, base, deviceID, legacyDeviceID, enrollOptions{ForceNew: true})
		}
	}
	return info, err
}

func defaultPromptRecovery(err *RecoveryAvailableError) bool {
	style := lipgloss.NewStyle()
	heading := style.Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("? Device Recovery Available")
	dim := style.Foreground(lipgloss.Color("#6B6B6B"))

	fmt.Printf("\n%s\n", heading)
	fmt.Printf("  %s %s\n",
		dim.Render("Device:"),
		err.DisplayName)
	fmt.Printf("  %s %s\n",
		dim.Render("Platform:"),
		err.Platform)
	if err.LastConnectedAt != "" {
		fmt.Printf("  %s %s\n",
			dim.Render("Last seen:"),
			err.LastConnectedAt)
	}
	fmt.Printf("  %s\n", dim.Render("This may be your previous device. Recover its identity?"))

	fmt.Printf("\n%s ",
		lipgloss.NewStyle().Bold(true).Render("Recover? [Y/n]:"))
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "" || input == "y" || input == "yes"
}

func (c *Client) Register(ctx context.Context) (*DeviceInfo, error) {
	existing, err := LoadDevice()
	if err != nil {
		return nil, err
	}

	if existing != nil {
		if !deviceV2FileExists() {
			// Only device.json exists (no v2) → old device, needs migration
			return c.migrateDeviceID(ctx, existing)
		}

		// device_v2.json exists → already migrated or fresh v2 device
		if ownerErr := c.validateOwner(existing); ownerErr != nil {
			_ = ClearDeviceV2()
		} else {
			// Validate user credentials before returning cached device info,
			// so expired/invalid auth is caught early instead of silently failing later.
			if _, authErr := c.authenticate(ctx); authErr != nil {
				return nil, authErr
			}

			// Check if the machine fingerprint has changed (e.g. MAC address swap).
			// If so, record the new fingerprint server-side for future recovery.
			currentLegacy := c.newLegacyID()
			if existing.LegacyDeviceID == "" {
				existing.LegacyDeviceID = currentLegacy
				_ = SaveDevice(existing)
			} else if currentLegacy != existing.LegacyDeviceID {
				logger.Info("[device] fingerprint changed (old=%s new=%s), updating server",
					existing.LegacyDeviceID, currentLegacy)
				if err := c.updateFingerprint(ctx, existing.DeviceID, currentLegacy); err != nil {
					logger.Warn("[device] failed to update fingerprint: %v", err)
				} else {
					existing.LegacyDeviceID = currentLegacy
					_ = SaveDevice(existing)
				}
			}

			resolved := c.cloud.CloudBaseURL("")
			if resolved != existing.BaseURL {
				existing.BaseURL = resolved
				_ = SaveDevice(existing)
			}
			return existing, nil
		}
	}

	creds, err := c.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	base := c.cloud.CloudBaseURL(creds.BaseURL)
	deviceID := c.newDeviceID()
	legacyDeviceID := c.newLegacyID()

	info, err := c.doEnroll(ctx, creds, base, deviceID, legacyDeviceID)
	if err != nil {
		if IsAuthError(err) && creds.RefreshToken != "" {
			creds, err = renew(ctx, c.cloud, creds)
			if err != nil {
				return nil, err
			}
			info, err = c.doEnroll(ctx, creds, base, deviceID, legacyDeviceID)
		}
	}
	if err != nil {
		return nil, err
	}

	return info, nil
}

// migrateDeviceID handles the one-time migration from old device.json to device_v2.json.
func (c *Client) migrateDeviceID(ctx context.Context, existing *DeviceInfo) (*DeviceInfo, error) {
	creds, err := c.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	base := c.cloud.CloudBaseURL(creds.BaseURL)
	newID := c.newDeviceID()

	info, err := c.doEnroll(ctx, creds, base, newID, existing.DeviceID)
	if err != nil {
		if IsAuthError(err) {
			return existing, err
		}
		logger.Warn("[device] migration failed (transient), using existing device: %v", err)
		return existing, nil
	}

	// Migration succeeded — save went to device_v2.json, clean up old device.json
	if err := ClearDevice(); err != nil {
		logger.Warn("[device] failed to remove old device.json: %v", err)
	}

	info.MigratedFrom = existing.DeviceID
	return info, nil
}

func auth(ctx context.Context, cc *cloud.Client) (*provider.Credentials, error) {
	creds, err := provider.LoadCredentials()
	if err != nil {
		return nil, err
	}
	if creds == nil || creds.AccessToken == "" {
		return nil, fmt.Errorf("not logged in: auth.json not found or access_token missing")
	}
	if creds.RefreshToken == "" || provider.IsTokenValid(creds.AccessToken, creds.RefreshToken, creds.ExpiryDate) {
		return creds, nil
	}
	return renew(ctx, cc, creds)
}

func renew(ctx context.Context, cc *cloud.Client, creds *provider.Credentials) (*provider.Credentials, error) {
	if creds.RefreshToken == "" {
		return creds, nil
	}
	baseURL := cc.OIDCBaseURL(creds.BaseURL)
	result, err := provider.RefreshCoStrictToken(baseURL, creds.RefreshToken, creds.State)
	if err != nil {
		return nil, err
	}
	expiry := provider.ExtractExpiryFromJWT(result.AccessToken)
	id := creds.ID
	if claims, err := provider.ParseJWT(result.AccessToken); err == nil {
		if uid := claims.UserID(); uid != "" {
			id = uid
		}
	}
	fresh := &provider.Credentials{
		ID:           id,
		Name:         creds.Name,
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		State:        creds.State,
		MachineID:    creds.MachineID,
		BaseURL:      baseURL,
		ExpiryDate:   expiry,
		UpdatedAt:    time.Now().Format(time.RFC3339),
		ExpiredAt:    time.UnixMilli(expiry).Format(time.RFC3339),
	}
	if err := provider.SaveCredentials(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

func enroll(ctx context.Context, cc *cloud.Client, creds *provider.Credentials, base, deviceID, legacyDeviceID string, opts enrollOptions) (*DeviceInfo, error) {
	reqBody := registerRequest{
		DeviceID:        deviceID,
		LegacyDeviceID:  legacyDeviceID,
		DisplayName:     hostname(),
		Platform:        runtime.GOOS + "-" + runtime.GOARCH,
		Version:         version.Get(),
		ConfirmRecovery: opts.ConfirmRecovery,
		ForceNew:        opts.ForceNew,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := cc.URL(cloud.PathDeviceRegister, base)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	cc.SetUserAuthHeaders(req, creds.AccessToken)

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 409 {
		return handleConflict(resp, base, creds.ID)
	}

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &RegistrationError{StatusCode: resp.StatusCode, Message: string(respBody), URL: url}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &RegistrationError{StatusCode: resp.StatusCode, Message: string(respBody), URL: url}
	}

	var out registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	info := &DeviceInfo{
		DeviceID:       deviceID,
		DeviceToken:    out.Token,
		AuthUserID:     creds.ID,
		RegisteredAt:   time.Now().Format(time.RFC3339),
		BaseURL:        base,
		LegacyDeviceID: legacyDeviceID,
	}
	if err := SaveDevice(info); err != nil {
		return nil, err
	}
	return info, nil
}

func handleConflict(resp *http.Response, base, authUserID string) (*DeviceInfo, error) {
	var conflict conflictResponse
	if err := json.NewDecoder(resp.Body).Decode(&conflict); err != nil {
		return nil, fmt.Errorf("device already registered")
	}
	if conflict.RecoveryAvailable && conflict.RecoverableDevice != nil {
		return nil, &RecoveryAvailableError{
			RecoverableDeviceID: conflict.RecoverableDevice.DeviceID,
			DisplayName:         conflict.RecoverableDevice.DisplayName,
			Platform:            conflict.RecoverableDevice.Platform,
			LastConnectedAt:     conflict.RecoverableDevice.LastConnectedAt,
		}
	}
	if conflict.Token != "" && conflict.Device != nil && conflict.Device.DeviceID != "" {
		info := &DeviceInfo{
			DeviceID:     conflict.Device.DeviceID,
			DeviceToken:  conflict.Token,
			AuthUserID:   authUserID,
			RegisteredAt: time.Now().Format(time.RFC3339),
			BaseURL:      base,
		}
		if err := SaveDevice(info); err != nil {
			return nil, err
		}
		return info, nil
	}
	if conflict.Error != "" {
		return nil, fmt.Errorf("%s", conflict.Error)
	}
	return nil, fmt.Errorf("device already registered")
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "401") || contains(msg, "403") || contains(msg, "token refresh failed")
}

func IsMissingAuthError(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "not logged in")
}

func IsExpiredAuthError(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "invalid or expired")
}

func IsInvalidDeviceTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "device token validation failed: 401") || contains(msg, "device token validation failed: 403")
}



func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "cs-cloud"
	}
	return h
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && containsStr(s, sub)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type registerRequest struct {
	DeviceID        string `json:"deviceId"`
	LegacyDeviceID  string `json:"legacyDeviceId"`
	DisplayName     string `json:"displayName"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	ConfirmRecovery bool   `json:"confirmRecovery,omitempty"`
	ForceNew        bool   `json:"forceNew,omitempty"`
}

type registerResponse struct {
	Device struct {
		DeviceID string `json:"deviceId"`
	} `json:"device"`
	Token string `json:"token"`
}

type conflictResponse struct {
	Device           *struct {
		DeviceID string `json:"deviceId"`
	} `json:"device"`
	Token             string `json:"token"`
	Error             string `json:"error"`
	RecoveryAvailable bool `json:"recoveryAvailable"`
	RecoverableDevice *struct {
		DeviceID        string `json:"deviceId"`
		DisplayName     string `json:"displayName"`
		Platform        string `json:"platform"`
		LastConnectedAt string `json:"lastConnectedAt"`
	} `json:"recoverableDevice"`
}

var _ error = (*RegistrationError)(nil)

type RegistrationError struct {
	StatusCode int
	Message    string
	URL        string
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("device registration failed: %d %s", e.StatusCode, e.Message)
}

func GetRegistrationURL(err error) string {
	if e, ok := err.(*RegistrationError); ok {
		return e.URL
	}
	return ""
}

func ClearDevice() error {
	p, err := DevicePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func ValidateDeviceOwner(info *DeviceInfo) error {
	if info == nil || info.AuthUserID == "" {
		return nil
	}
	cred, err := provider.LoadCredentials()
	if err != nil || cred == nil {
		return fmt.Errorf("auth user changed: no credentials found")
	}
	if cred.ID != info.AuthUserID {
		return fmt.Errorf("auth user changed: device bound to %q but current user is %q", info.AuthUserID, cred.ID)
	}
	return nil
}

func ReRegister(ctx context.Context, cfg *config.Config) (*DeviceInfo, error) {
	_ = ClearDevice()
	_ = ClearDeviceV2()
	c := NewClient(cfg)
	return c.Register(ctx)
}

func updateFingerprint(ctx context.Context, cc *cloud.Client, deviceID, legacyDeviceID string) error {
	cred, err := provider.LoadCredentials()
	if err != nil || cred == nil || cred.AccessToken == "" {
		return fmt.Errorf("not logged in")
	}
	base := cc.CloudBaseURL(cred.BaseURL)
	url := cc.URL(cloud.DeviceFingerprintPath(deviceID), base)

	body, _ := json.Marshal(map[string]string{"legacyDeviceId": legacyDeviceID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	cc.SetUserAuthHeaders(req, cred.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("update fingerprint failed: %d %s", resp.StatusCode, string(respBody))
}
