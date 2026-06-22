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

const testGatewayAssignPath = "/cloud/device/gateway-assign"

func isGatewayAssignRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, testGatewayAssignPath)
}

func TestCheckGatewayConnectivity_Success(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		http.NotFound(w, r)
	}))
	defer gatewaySrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGatewayAssignRequest(r) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheckGatewayConnectivity_AssignGatewayFails(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway-assign fails")
	}
}

func TestCheckGatewayConnectivity_GatewayUnreachable(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGatewayAssignRequest(r) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": "http://127.0.0.1:1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway is unreachable")
	}
}

func TestCheckGatewayConnectivity_GatewayReturns500(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gatewaySrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGatewayAssignRequest(r) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway returns 500")
	}
}

func TestCheckGatewayConnectivity_AuthHeadersSent(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer gatewaySrv.Close()

	var receivedAuth string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "my-secret-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected auth header 'Bearer my-secret-token', got %q", receivedAuth)
	}
}

func TestCheckGatewayConnectivity_ContextCancelled(t *testing.T) {
	blockCh := make(chan struct{})
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.WriteHeader(http.StatusOK)
	}))
	defer apiSrv.Close()
	defer close(blockCh)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL + "/cloud-api",
	}

	err := CheckGatewayConnectivity(ctx, dev)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

// ── GatewayAssignError ─────────────────────────────────────────────────

func TestGatewayAssignError_ErrorMessage(t *testing.T) {
	err := &GatewayAssignError{StatusCode: 429, Message: "too many requests"}
	want := "gateway-assign failed: 429 too many requests"
	if got := err.Error(); got != want {
		t.Errorf("GatewayAssignError.Error() = %q, want %q", got, want)
	}
}

func TestGatewayAssignError_RetryAfterSeconds(t *testing.T) {
	err := &GatewayAssignError{RetryAfter: "120"}
	d, ok := err.RetryAfterDuration()
	if !ok {
		t.Fatal("RetryAfterDuration() should return true for seconds format")
	}
	if d != 120*time.Second {
		t.Errorf("RetryAfterDuration() = %v, want %v", d, 120*time.Second)
	}
}

func TestGatewayAssignError_RetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(5 * time.Minute)
	err := &GatewayAssignError{RetryAfter: future.Format(time.RFC1123)}
	d, ok := err.RetryAfterDuration()
	if !ok {
		t.Fatal("RetryAfterDuration() should return true for HTTP-date format")
	}
	if d <= 0 {
		t.Fatal("RetryAfterDuration() should return positive duration for future date")
	}
}

func TestGatewayAssignError_RetryAfterPastHTTPDate(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	err := &GatewayAssignError{RetryAfter: past.Format(time.RFC1123)}
	d, ok := err.RetryAfterDuration()
	if !ok {
		t.Fatal("RetryAfterDuration() should return true for HTTP-date even if in the past")
	}
	if d != 0 {
		t.Errorf("RetryAfterDuration() for past date = %v, want 0", d)
	}
}

func TestGatewayAssignError_RetryAfterInvalid(t *testing.T) {
	err := &GatewayAssignError{RetryAfter: "not-a-valid-value"}
	_, ok := err.RetryAfterDuration()
	if ok {
		t.Error("RetryAfterDuration() should return false for invalid Retry-After")
	}
}

func TestGatewayAssignError_RetryAfterEmpty(t *testing.T) {
	err := &GatewayAssignError{RetryAfter: ""}
	_, ok := err.RetryAfterDuration()
	if ok {
		t.Error("RetryAfterDuration() should return false for empty Retry-After")
	}
}

func TestGatewayAssignError_RetryAfterZero(t *testing.T) {
	err := &GatewayAssignError{RetryAfter: "0"}
	_, ok := err.RetryAfterDuration()
	if ok {
		t.Error("RetryAfterDuration() should return false for Retry-After=0")
	}
}

func TestGatewayAssignError_RetryAfterNegative(t *testing.T) {
	err := &GatewayAssignError{RetryAfter: "-1"}
	_, ok := err.RetryAfterDuration()
	if ok {
		t.Error("RetryAfterDuration() should return false for negative Retry-After")
	}
}
