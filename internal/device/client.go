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
	"runtime"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
)

type Client struct {
	cfg   *config.Config
	cloud *cloud.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{cfg: cfg, cloud: cloud.NewClient(cfg)}
}

func (c *Client) CloudBaseURL() string {
	cred, err := provider.LoadCredentials()
	if err != nil || cred == nil {
		return c.cloud.CloudBaseURL("")
	}
	return c.cloud.CloudBaseURL(cred.BaseURL)
}

func (c *Client) Register(ctx context.Context) (*DeviceInfo, error) {
	existing, err := LoadDevice()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if ownerErr := ValidateDeviceOwner(existing); ownerErr != nil {
			_ = ClearDevice()
		} else {
			resolved := c.cloud.CloudBaseURL("")
			if resolved != existing.BaseURL {
				existing.BaseURL = resolved
				_ = SaveDevice(existing)
			}
			return existing, nil
		}
	}

	creds, err := auth(ctx, c.cloud)
	if err != nil {
		return nil, err
	}

	base := c.cloud.CloudBaseURL(creds.BaseURL)
	deviceID := GetDeviceID()
	legacyDeviceID := GetLegacyDeviceID()

	info, err := enroll(ctx, c.cloud, creds, base, deviceID, legacyDeviceID)
	if err != nil {
		if IsAuthError(err) && creds.RefreshToken != "" {
			creds, err = renew(ctx, c.cloud, creds)
			if err != nil {
				return nil, err
			}
			info, err = enroll(ctx, c.cloud, creds, base, deviceID, legacyDeviceID)
		}
	}
	if err != nil {
		return nil, err
	}

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

func enroll(ctx context.Context, cc *cloud.Client, creds *provider.Credentials, base, deviceID, legacyDeviceID string) (*DeviceInfo, error) {
	reqBody := registerRequest{
		DeviceID:       deviceID,
		LegacyDeviceID: legacyDeviceID,
		DisplayName:    hostname(),
		Platform:       runtime.GOOS + "-" + runtime.GOARCH,
		Version:        version.Get(),
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
		DeviceID:     deviceID,
		DeviceToken:  out.Token,
		AuthUserID:   creds.ID,
		RegisteredAt: time.Now().Format(time.RFC3339),
		BaseURL:      base,
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
	if conflict.Token != "" && conflict.Device != nil && conflict.Device.DeviceID != "" {
		info := &DeviceInfo{
			DeviceID:     GetDeviceID(),
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
	return contains(msg, "401") || contains(msg, "403")
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
	DeviceID       string `json:"deviceId"`
	LegacyDeviceID string `json:"legacyDeviceId"`
	DisplayName    string `json:"displayName"`
	Platform       string `json:"platform"`
	Version        string `json:"version"`
}

type registerResponse struct {
	Device struct {
		DeviceID string `json:"deviceId"`
	} `json:"device"`
	Token string `json:"token"`
}

type conflictResponse struct {
	Device *struct {
		DeviceID string `json:"deviceId"`
	} `json:"device"`
	Token string `json:"token"`
	Error string `json:"error"`
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
	c := NewClient(cfg)
	return c.Register(ctx)
}
