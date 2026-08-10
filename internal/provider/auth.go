package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/platform"
)

func GenerateState() string {
	b1 := make([]byte, 6)
	b2 := make([]byte, 6)
	_, _ = rand.Read(b1)
	_, _ = rand.Read(b2)
	return fmt.Sprintf("%x.%x", b1, b2)
}

func BuildLoginURL(baseURL, state, machineID string) string {
	params := BuildOAuthParams(true, machineID, state)
	qs := strings.Join(params, "&")
	return fmt.Sprintf("%s/oidc-auth/api/v1/plugin/login?%s", baseURL, qs)
}

func BuildTokenPollURL(baseURL, state, machineID string) string {
	params := BuildOAuthParams(true, machineID, state)
	qs := strings.Join(params, "&")
	return fmt.Sprintf("%s/oidc-auth/api/v1/plugin/login/token?%s", baseURL, qs)
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type pollResult struct {
	Success bool      `json:"success"`
	Data    *pollData `json:"data"`
	Message string    `json:"message"`
}

type pollData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	State        string `json:"state"`
}

func PollLoginToken(ctx context.Context, baseURL, state, machineID string) (*TokenResponse, error) {
	tokenURL := BuildTokenPollURL(baseURL, state, machineID)
	interval := 5 * time.Second
	maxAttempts := 120

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("login cancelled")
		case <-time.After(interval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")

		resp, err := platform.HTTPClient().Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			continue
		}

		var result pollResult
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		if !result.Success {
			msg := result.Message
			if strings.Contains(msg, "invalid") || strings.Contains(msg, "expired") || strings.Contains(msg, "failed") {
				return nil, fmt.Errorf("login failed: %s", msg)
			}
			continue
		}

		if result.Data == nil || result.Data.AccessToken == "" || result.Data.RefreshToken == "" {
			continue
		}

		if result.Data.State != state {
			continue
		}

		return &TokenResponse{
			AccessToken:  result.Data.AccessToken,
			RefreshToken: result.Data.RefreshToken,
		}, nil
	}

	return nil, fmt.Errorf("login timeout after %d seconds", maxAttempts*5)
}

func LoginCoStrict(ctx context.Context) (*Credentials, error) {
	cc := cloud.NewClient(nil)
	baseURL := cc.OIDCBaseURL("")
	state := GenerateState()
	machineID := GenerateMachineID()

	loginURL := BuildLoginURL(baseURL, state, machineID)
	fmt.Println("[CoStrict] Opening browser for login...")
	fmt.Printf("[CoStrict] Login URL: %s\n", loginURL)

	if err := openBrowser(loginURL); err != nil {
		fmt.Printf("[CoStrict] Could not open browser: %v\n", err)
		fmt.Println("[CoStrict] Please open the URL above manually.")
	}

	tokens, err := PollLoginToken(ctx, baseURL, state, machineID)
	if err != nil {
		return nil, err
	}

	expiry := ExtractExpiryFromJWT(tokens.AccessToken)
	now := time.Now()

	var userID string
	if claims, err := ParseJWT(tokens.AccessToken); err == nil {
		userID = claims.UserID()
	}
	if userID == "" {
		userID = machineID
	}

	cred := &Credentials{
		ID:           userID,
		Name:         "cs-cloud",
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		State:        state,
		MachineID:    machineID,
		BaseURL:      baseURL,
		ExpiryDate:   expiry,
		UpdatedAt:    now.Format(time.RFC3339),
		ExpiredAt:    time.UnixMilli(expiry).Format(time.RFC3339),
	}

	if err := SaveCredentials(cred); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}

	return cred, nil
}

type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func RefreshCoStrictToken(baseURL, refreshToken, state string) (*RefreshTokenResponse, error) {
	params := BuildOAuthParams(false, "", state)
	qs := strings.Join(params, "&")
	tokenURL := fmt.Sprintf("%s/oidc-auth/api/v1/plugin/login/token?%s", baseURL, qs)

	req, err := http.NewRequest(http.MethodGet, tokenURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("Accept", "application/json")

	resp, err := platform.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 400 || resp.StatusCode == 401 {
		return nil, fmt.Errorf("refresh token is invalid or expired")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg != "" {
			return nil, fmt.Errorf("token refresh failed: %d (%s)", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("token refresh failed: %d", resp.StatusCode)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}

	data := raw
	if d, ok := raw["data"].(map[string]any); ok {
		data = d
	}

	at, _ := data["access_token"].(string)
	rt, _ := data["refresh_token"].(string)
	if at == "" || rt == "" {
		return nil, fmt.Errorf("refresh response missing tokens")
	}

	return &RefreshTokenResponse{AccessToken: at, RefreshToken: rt}, nil
}

func openBrowser(u string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{u}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", u}
	default:
		cmd = "xdg-open"
		args = []string{u}
	}

	return exec.Command(cmd, args...).Start()
}
