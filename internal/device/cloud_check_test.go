package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetDeviceCloudStatus(t *testing.T) {
	tests := []struct {
		name       string
		respStatus int
		respBody   any
		wantErr    string
		wantOnline bool
		wantStatus string
		checkLast  bool
	}{
		{
			name:       "online device",
			respStatus: http.StatusOK,
			respBody: map[string]any{
				"device": map[string]any{
					"deviceId":        "dev-1",
					"status":          "online",
					"lastConnectedAt": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
					"lastSeenAt":      time.Now().Add(-3 * time.Second).Format(time.RFC3339),
				},
			},
			wantOnline: true,
			wantStatus: "online",
			checkLast:  true,
		},
		{
			name:       "offline device",
			respStatus: http.StatusOK,
			respBody: map[string]any{
				"device": map[string]any{
					"deviceId": "dev-1",
					"status":   "offline",
				},
			},
			wantOnline: false,
			wantStatus: "offline",
		},
		{
			name:       "empty status treated as offline",
			respStatus: http.StatusOK,
			respBody: map[string]any{
				"device": map[string]any{
					"deviceId": "dev-1",
					"status":   "",
				},
			},
			wantOnline: false,
			wantStatus: "",
		},
		{
			name:       "404 returns not-found error",
			respStatus: http.StatusNotFound,
			respBody:   map[string]any{"error": "device not found"},
			wantErr:    "device not found on cloud",
		},
		{
			name:       "500 returns status error",
			respStatus: http.StatusInternalServerError,
			respBody:   map[string]any{"error": "boom"},
			wantErr:    "unexpected status 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.respStatus)
				json.NewEncoder(w).Encode(tt.respBody)
			}))
			defer srv.Close()

			t.Setenv("COSTRICT_CLOUD_BASE_URL", srv.URL)

			dev := &DeviceInfo{DeviceID: "dev-1"}
			status, err := GetDeviceCloudStatus(context.Background(), dev, "access-token")

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status.Online != tt.wantOnline {
				t.Errorf("Online = %v, want %v", status.Online, tt.wantOnline)
			}
			if status.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", status.Status, tt.wantStatus)
			}
			if tt.checkLast {
				if status.LastSeenAt == nil {
					t.Errorf("LastSeenAt = nil, want non-nil")
				}
				if status.SecondsSinceLastSeen <= 0 {
					t.Errorf("SecondsSinceLastSeen = %d, want > 0", status.SecondsSinceLastSeen)
				}
			}
		})
	}
}

func TestGetDeviceCloudStatus_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	t.Setenv("COSTRICT_CLOUD_BASE_URL", srv.URL)

	dev := &DeviceInfo{DeviceID: "dev-1"}
	_, err := GetDeviceCloudStatus(context.Background(), dev, "access-token")
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse device response") {
		t.Fatalf("error = %q, want parse error", err.Error())
	}
}

func TestGetDeviceCloudStatus_NilDevice(t *testing.T) {
	_, err := GetDeviceCloudStatus(context.Background(), nil, "token")
	if err == nil || !strings.Contains(err.Error(), "no device info") {
		t.Fatalf("error = %v, want 'no device info'", err)
	}
}

func TestGetDeviceCloudStatus_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device": map[string]any{"deviceId": "dev-1", "status": "online"},
		})
	}))
	defer srv.Close()

	t.Setenv("COSTRICT_CLOUD_BASE_URL", srv.URL)

	dev := &DeviceInfo{DeviceID: "dev-1"}
	_, _ = GetDeviceCloudStatus(context.Background(), dev, "my-token")
	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-token")
	}
}
