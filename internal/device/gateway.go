package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/version"
)

// GatewayAssignError 包含 gateway-assign HTTP 错误的状态码和 Retry-After 信息
type GatewayAssignError struct {
	StatusCode int
	Message    string
	RetryAfter string // 原始 Retry-After 头值（秒数或 HTTP-date）
}

func (e *GatewayAssignError) Error() string {
	return fmt.Sprintf("gateway-assign failed: %d %s", e.StatusCode, e.Message)
}

// RetryAfterDuration 解析 Retry-After 头，返回等待时长
// 支持：秒数（"120"）和 HTTP-date（"Wed, 21 Oct 2015 07:28:00 GMT"）
func (e *GatewayAssignError) RetryAfterDuration() (time.Duration, bool) {
	if e.RetryAfter == "" {
		return 0, false
	}
	// 先尝试解析为秒数
	if seconds, err := strconv.Atoi(e.RetryAfter); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second, true
	}
	// 尝试解析为 HTTP-date
	if t, err := time.Parse(time.RFC1123, e.RetryAfter); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// IsGatewayAssignRateLimitError 判断是否是 gateway-assign 的限流错误（429）
func IsGatewayAssignRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	var gwErr *GatewayAssignError
	if errors.As(err, &gwErr) {
		return gwErr.StatusCode == http.StatusTooManyRequests
	}
	return contains(err.Error(), "gateway-assign failed: 429")
}

// IsGatewayAssignAuthError 检查是否是 gateway-assign 的认证错误（需要重新注册）
func IsGatewayAssignAuthError(err error) bool {
	if err == nil {
		return false
	}
	var gwErr *GatewayAssignError
	if errors.As(err, &gwErr) {
		return gwErr.StatusCode == http.StatusUnauthorized || gwErr.StatusCode == http.StatusForbidden
	}
	msg := err.Error()
	return contains(msg, "gateway-assign failed: 401") || contains(msg, "gateway-assign failed: 403")
}

func CheckGatewayConnectivity(ctx context.Context, dev *DeviceInfo) error {
	gatewayURL, err := AssignGateway(ctx, dev)
	if err != nil {
		return fmt.Errorf("gateway assignment failed: %w", err)
	}

	healthURL := strings.TrimRight(gatewayURL, "/") + "/health"
	healthCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return nil
	}

	cc := cloud.NewClient(nil)
	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("gateway unreachable (%s): %w", gatewayURL, err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("gateway returned %d", resp.StatusCode)
	}

	return nil
}

func ValidateDeviceToken(ctx context.Context, device *DeviceInfo) error {
	reqBody := map[string]any{
		"deviceID": device.DeviceID,
		"version":  version.Get(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.PathGatewayAssign, device.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	cc.SetDeviceAuthHeadersWithUser(req, device.DeviceToken, userAccessToken())

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("device token validation failed: %d %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func AssignGateway(ctx context.Context, device *DeviceInfo) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqBody := map[string]any{
		"deviceID": device.DeviceID,
		"version":  version.Get(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.PathGatewayAssign, device.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	cc.SetDeviceAuthHeadersWithUser(req, device.DeviceToken, userAccessToken())

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", &GatewayAssignError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(respBody)),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}

	var data struct {
		GatewayURL string `json:"gatewayURL"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	return data.GatewayURL, nil
}
