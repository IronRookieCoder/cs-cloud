package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
)

type HeartbeatResponse struct {
	PendingCommands []CloudCommand `json:"pending_commands,omitempty"`
}

type CloudCommand struct {
	CommandID string          `json:"command_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
}

func (c *Client) HeartbeatWithResponse(ctx context.Context, tunnelConnected bool) (*HeartbeatResponse, error) {
	dev, err := LoadDevice()
	if err != nil || dev == nil {
		return nil, err
	}

	if ownerErr := ValidateDeviceOwner(dev); ownerErr != nil {
		logger.Warn("[heartbeat] %v, attempting re-registration...", ownerErr)
		dev, err = ReRegister(ctx, c.cfg)
		if err != nil {
			return nil, fmt.Errorf("re-register failed: %w", err)
		}
		logger.Info("[heartbeat] device re-registered successfully (device_id=%s)", dev.DeviceID)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	body, _ := json.Marshal(map[string]any{
		"deviceID":        dev.DeviceID,
		"version":         version.Get(),
		"tunnelConnected": tunnelConnected,
	})

	url := c.cloud.URL(cloud.DeviceHeartbeatPath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.cloud.SetDeviceAuthHeadersWithUser(req, dev.DeviceToken, userAccessToken())

	resp, err := c.cloud.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("heartbeat failed: %d %s", resp.StatusCode, string(respBody))
	}

	var hbResp HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err != nil {
		return nil, err
	}
	return &hbResp, nil
}

func HeartbeatLoop(ctx context.Context, cfg *config.Config, tunnelStatus func() bool, onCommands func([]CloudCommand)) func(connected bool) {
	client := NewClient(cfg)
	interval := 5 * time.Minute

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	send := func(connected bool) {
		if !connected {
			logger.Info("[heartbeat] sending heartbeat with tunnelConnected=false")
		}
		resp, err := client.HeartbeatWithResponse(ctx, connected)
		if err != nil {
			logger.Warn("[heartbeat] failed: %v", err)
			return
		}
		if onCommands != nil && len(resp.PendingCommands) > 0 {
			onCommands(resp.PendingCommands)
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c := false
				if tunnelStatus != nil {
					c = tunnelStatus()
				}
				send(c)
			}
		}
	}()

	return func(c bool) {
		send(c)
	}
}

func userAccessToken() string {
	if cred, err := provider.LoadCredentials(); err == nil && cred != nil && cred.AccessToken != "" {
		return cred.AccessToken
	}
	return ""
}
