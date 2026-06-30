package device

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cs-cloud/internal/cloud"
)

type CloudDeviceStatus struct {
	DeviceID             string     `json:"deviceId"`
	Online               bool       `json:"online"`
	Status               string     `json:"status"`
	LastConnectedAt      *time.Time `json:"lastConnectedAt,omitempty"`
	LastSeenAt           *time.Time `json:"lastSeenAt,omitempty"`
	SecondsSinceLastSeen int        `json:"secondsSinceLastSeen,omitempty"`
}

func IsDeviceRegisteredOnCloud(ctx context.Context, dev *DeviceInfo, userAccessToken string) (bool, error) {
	if dev == nil {
		return false, fmt.Errorf("no device info")
	}

	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.DeviceGetPath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	cc.SetUserAuthHeaders(req, userAccessToken)

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

func GetDeviceCloudStatus(ctx context.Context, dev *DeviceInfo, userAccessToken string) (*CloudDeviceStatus, error) {
	if dev == nil {
		return nil, fmt.Errorf("no device info")
	}

	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.DeviceGetPath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	cc.SetUserAuthHeaders(req, userAccessToken)

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("device not found on cloud")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Device struct {
			DeviceID        string     `json:"deviceId"`
			Status          string     `json:"status"`
			LastConnectedAt *time.Time `json:"lastConnectedAt"`
			LastSeenAt      *time.Time `json:"lastSeenAt"`
		} `json:"device"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse device response: %w", err)
	}

	d := envelope.Device
	status := &CloudDeviceStatus{
		DeviceID:        d.DeviceID,
		Status:          d.Status,
		Online:          d.Status == "online",
		LastConnectedAt: d.LastConnectedAt,
		LastSeenAt:      d.LastSeenAt,
	}
	if d.LastSeenAt != nil {
		status.SecondsSinceLastSeen = int(time.Since(*d.LastSeenAt).Seconds())
	}
	return status, nil
}
