package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/device"
	"cs-cloud/internal/provider"
)

type checkResult struct {
	name   string
	ok     bool
	detail string
	err    string
	fix    fixFunc
	hint   string
}

type fixFunc func(ctx context.Context, a *app.App) *checkResult

func (r *checkResult) print() {
	if r.ok {
		printSuccess("%s %s", r.name, dimDetail(r.detail))
	} else {
		printError("%s %s", r.name, dimDetail(r.err))
		if r.hint != "" {
			printInfo("%s", r.hint)
		}
	}
}

func dimDetail(s string) string {
	if s == "" {
		return ""
	}
	return dimStyle.Render(s)
}

func doctor(a *app.App) error {
	printTitle("doctor")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var fixables []struct {
		idx int
		r   *checkResult
	}
	failCount := 0

	checks := []struct {
		name string
		fn   func(ctx context.Context, a *app.App) *checkResult
	}{
		{"daemon", checkDaemon},
		{"instance conflict", checkInstanceConflict},
		{"cloud connectivity", checkCloudConnectivity},
		{"credentials", checkCredentials},
		{"device", checkDevice},
		{"device registered on cloud", checkDeviceOnCloud},
		{"device token", checkDeviceToken},
		{"gateway", checkGateway},
		{"tunnel", checkTunnel},
	}

	for i, c := range checks {
		r := c.fn(ctx, a)
		r.print()
		if !r.ok {
			failCount++
			if r.fix != nil {
				fixables = append(fixables, struct {
					idx int
					r   *checkResult
				}{idx: i, r: r})
			}
			if c.name == "instance conflict" || c.name == "cloud connectivity" || c.name == "credentials" {
				break
			}
		}
	}

	fmt.Println()

	if failCount == 0 {
		printSuccess("No issues found.")
		return nil
	}

	if len(fixables) == 0 {
		printError("%d check(s) failed, no auto-fix available.", failCount)
		printInfo("Check the errors above or run 'logs' for details.")
		return nil
	}

	printInfo("Applying %d fix(es)...", len(fixables))

	fmt.Println()
	applied := 0
	for _, f := range fixables {
		printInfo("Fixing: %s...", f.r.name)
		result := f.r.fix(ctx, a)
		result.print()
		if result.ok {
			applied++
		} else {
			if result.hint != "" {
				printInfo("%s", result.hint)
			}
		}
		fmt.Println()
	}

	if applied == len(fixables) {
		printSuccess("All %d fix(es) applied successfully.", applied)
	} else {
		printWarn("%d/%d fix(es) applied.", applied, len(fixables))
	}

	return nil
}

func checkDaemon(_ context.Context, a *app.App) *checkResult {
	running, pid, reason := a.DaemonStatus()
	if running {
		return &checkResult{name: "daemon", ok: true, detail: fmt.Sprintf("(pid=%d)", pid)}
	}
	return &checkResult{
		name: "daemon not running",
		err:  reason,
		hint: "Run 'start' to start the service",
	}
}

func checkInstanceConflict(_ context.Context, a *app.App) *checkResult {
	conflicts := a.FindConflictingInstances()
	if len(conflicts) == 0 {
		return &checkResult{name: "instance conflict", ok: true, detail: "no conflicts"}
	}

	pids := make([]string, len(conflicts))
	for i, c := range conflicts {
		pids[i] = fmt.Sprintf("%d", c.PID)
	}
	return &checkResult{
		name: "instance conflict",
		err:  fmt.Sprintf("%d conflicting process(es): pid=%s", len(conflicts), fmt.Sprintf("%v", pids)),
		fix:  fixInstanceConflict(conflicts),
		hint: "Multiple cs-cloud instances may compete for the same resources",
	}
}

func fixInstanceConflict(conflicts []app.ConflictingInstance) fixFunc {
	return func(_ context.Context, a *app.App) *checkResult {
		cleaned := a.ForceCleanupStale()
		if cleaned {
			return &checkResult{name: "instance conflict", ok: true, detail: "conflicting processes terminated"}
		}
		return &checkResult{
			name: "instance conflict",
			err:  "failed to terminate conflicting processes",
			hint: "Try running 'stop' then 'start'",
		}
	}
}

func checkCloudConnectivity(ctx context.Context, a *app.App) *checkResult {
	cc := cloud.NewClient(a.Config())
	cred, _ := a.Credentials()
	credBaseURL := ""
	if cred != nil {
		credBaseURL = cred.BaseURL
	}
	baseURL := cc.CloudBaseURL(credBaseURL)

	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return &checkResult{name: "cloud connectivity", ok: false, err: err.Error()}
	}

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return &checkResult{
			name: "cloud connectivity",
			err:  fmt.Sprintf("cannot reach %s: %v", baseURL, err),
			hint: "Check your network connection",
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return &checkResult{name: "cloud connectivity", ok: false, err: fmt.Sprintf("server returned %d", resp.StatusCode)}
	}
	return &checkResult{name: "cloud connectivity", ok: true, detail: fmt.Sprintf("(%s)", baseURL)}
}

func checkCredentials(ctx context.Context, a *app.App) *checkResult {
	cred, err := a.Credentials()
	if err != nil {
		return &checkResult{name: "credentials", ok: false, err: err.Error()}
	}
	if cred == nil {
		return &checkResult{
			name: "credentials not found",
			err:  "not logged in",
			hint: "Run 'login' to authenticate",
		}
	}

	if provider.IsTokenValid(cred.AccessToken, cred.RefreshToken, cred.ExpiryDate) {
		return &checkResult{name: "credentials", ok: true, detail: fmt.Sprintf("(%s)", resolveCloudUser(ctx, a.Config(), cred))}
	}

	if cred.RefreshToken == "" {
		return &checkResult{
			name: "credentials expired",
			err:  "no refresh token available",
			hint: "Run 'login' to re-authenticate",
		}
	}

	return &checkResult{
		name: "credentials expired",
		err:  "access token expired",
		fix:  fixCredentials(cred),
	}
}

func fixCredentials(cred *provider.Credentials) fixFunc {
	return func(ctx context.Context, _ *app.App) *checkResult {
		cc := cloud.NewClient(nil)
		baseURL := cc.OIDCBaseURL(cred.BaseURL)
		result, refreshErr := provider.RefreshCoStrictToken(baseURL, cred.RefreshToken, cred.State)
		if refreshErr != nil {
			return &checkResult{
				name: "credentials refresh",
				err:  fmt.Sprintf("refresh failed: %v", refreshErr),
				hint: "Run 'login' to re-authenticate",
			}
		}

		expiry := provider.ExtractExpiryFromJWT(result.AccessToken)
		id := cred.ID
		if claims, parseErr := provider.ParseJWT(result.AccessToken); parseErr == nil {
			if uid := claims.UserID(); uid != "" {
				id = uid
			}
		}
		fresh := &provider.Credentials{
			ID:           id,
			Name:         cred.Name,
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
			State:        cred.State,
			MachineID:    cred.MachineID,
			BaseURL:      baseURL,
			ExpiryDate:   expiry,
			UpdatedAt:    time.Now().Format(time.RFC3339),
			ExpiredAt:    time.UnixMilli(expiry).Format(time.RFC3339),
		}
		if saveErr := provider.SaveCredentials(fresh); saveErr != nil {
			return &checkResult{name: "credentials refresh", err: fmt.Sprintf("save failed: %v", saveErr)}
		}
		return &checkResult{name: "credentials", ok: true, detail: fmt.Sprintf("refreshed (user=%s)", resolveUser(fresh))}
	}
}

func checkDevice(ctx context.Context, a *app.App) *checkResult {
	dev, err := a.Device()
	if err != nil {
		return &checkResult{name: "device", ok: false, err: err.Error()}
	}
	if dev == nil {
		return &checkResult{
			name: "device not registered",
			err:  "no device.json found",
			fix:  fixDevice,
		}
	}

	if ownerErr := device.ValidateDeviceOwner(dev); ownerErr != nil {
		return &checkResult{
			name: "device owner mismatch",
			err:  ownerErr.Error(),
			fix:  fixDevice,
		}
	}

	return &checkResult{name: "device", ok: true, detail: fmt.Sprintf("(id=%s, legacy=%s)", dev.DeviceID, device.GetLegacyDeviceID())}
}

func fixDevice(ctx context.Context, a *app.App) *checkResult {
	info, err := device.Register(ctx, a.Config())
	if err != nil {
		hint := ""
		if device.IsMissingAuthError(err) || device.IsExpiredAuthError(err) {
			hint = "Run 'login' first, then retry"
		}
		return &checkResult{
			name: "device registration",
			err:  fmt.Sprintf("registration failed: %v", err),
			hint: hint,
		}
	}
	return &checkResult{name: "device", ok: true, detail: fmt.Sprintf("registered (id=%s)", info.DeviceID)}
}

func checkDeviceOnCloud(ctx context.Context, a *app.App) *checkResult {
	dev, err := a.Device()
	if err != nil || dev == nil {
		return &checkResult{name: "device registered on cloud", ok: false, err: "no device"}
	}
	cred, err := a.Credentials()
	if err != nil || cred == nil {
		return &checkResult{name: "device registered on cloud", ok: false, err: "no credentials"}
	}

	found, checkErr := device.IsDeviceRegisteredOnCloud(ctx, dev, cred.AccessToken)
	if checkErr != nil {
		return &checkResult{name: "device registered on cloud", ok: false, err: checkErr.Error()}
	}
	if found {
		return &checkResult{name: "device registered on cloud", ok: true, detail: "found on server"}
	}
	return &checkResult{
		name: "device not found on cloud",
		err:  fmt.Sprintf("device %s not in cloud device list", shortDeviceID(dev.DeviceID)),
		fix:  fixDeviceOnCloud,
		hint: "The device may have been deleted from the cloud UI",
	}
}

func fixDeviceOnCloud(ctx context.Context, a *app.App) *checkResult {
	_ = device.ClearDevice()
	_ = device.ClearDeviceV2()
	info, err := device.Register(ctx, a.Config())
	if err != nil {
		hint := ""
		if device.IsMissingAuthError(err) || device.IsExpiredAuthError(err) {
			hint = "Run 'login' first, then retry"
		}
		return &checkResult{
			name: "device re-registration",
			err:  fmt.Sprintf("registration failed: %v", err),
			hint: hint,
		}
	}
	return &checkResult{name: "device registered on cloud", ok: true, detail: fmt.Sprintf("re-registered (id=%s)", info.DeviceID)}
}

func shortDeviceID(id string) string {
	if len(id) > 16 {
		return id[:16] + "..."
	}
	return id
}

func checkDeviceToken(ctx context.Context, a *app.App) *checkResult {
	dev, err := a.Device()
	if err != nil || dev == nil {
		return &checkResult{name: "device token", ok: false, err: "no device"}
	}

	validateErr := device.ValidateDeviceToken(ctx, dev)
	if validateErr == nil {
		return &checkResult{name: "device token", ok: true, detail: "valid"}
	}

	if device.IsInvalidDeviceTokenError(validateErr) {
		return &checkResult{
			name: "device token invalid",
			err:  "server rejected device token",
			fix:  fixDeviceToken,
		}
	}
	return &checkResult{name: "device token", ok: false, err: validateErr.Error()}
}

func fixDeviceToken(ctx context.Context, a *app.App) *checkResult {
	_ = device.ClearDevice()
	_ = device.ClearDeviceV2()
	info, regErr := device.Register(ctx, a.Config())
	if regErr != nil {
		hint := ""
		if device.IsMissingAuthError(regErr) || device.IsExpiredAuthError(regErr) {
			hint = "Run 'login' first, then retry"
		}
		return &checkResult{
			name: "device token fix",
			err:  fmt.Sprintf("re-register failed: %v", regErr),
			hint: hint,
		}
	}

	if tokenErr := device.ValidateDeviceToken(ctx, info); tokenErr != nil {
		return &checkResult{
			name: "device token fix",
			err:  fmt.Sprintf("still invalid after re-register: %v", tokenErr),
		}
	}
	return &checkResult{name: "device token", ok: true, detail: "re-registered and validated"}
}

func checkGateway(ctx context.Context, a *app.App) *checkResult {
	dev, err := a.Device()
	if err != nil || dev == nil {
		return &checkResult{name: "gateway", ok: false, err: "no device"}
	}

	gwErr := device.CheckGatewayConnectivity(ctx, dev)
	if gwErr == nil {
		return &checkResult{name: "gateway", ok: true, detail: "reachable"}
	}
	return &checkResult{
		name: "gateway unreachable",
		err:  gwErr.Error(),
		hint: "Check your network connection and try again",
	}
}

func checkTunnel(ctx context.Context, a *app.App) *checkResult {
	running, _, _ := a.DaemonStatus()
	if !running {
		return &checkResult{name: "tunnel", ok: false, err: "daemon not running"}
	}

	serverURL, err := a.ServerURL()
	if err != nil || serverURL == "" {
		return &checkResult{name: "tunnel", ok: false, err: "daemon server URL not available"}
	}

	mode := a.LoadMode()
	if mode != "cloud" {
		return &checkResult{name: "tunnel", ok: true, detail: "not in cloud mode"}
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, serverURL+"/api/v1/runtime/health", nil)
	if err != nil {
		return &checkResult{name: "tunnel", ok: false, err: err.Error()}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &checkResult{name: "tunnel", ok: false, err: fmt.Sprintf("cannot reach daemon: %v", err)}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var health struct {
		Data struct {
			Tunnel struct {
				Connected   bool       `json:"connected"`
				ConnectedAt *time.Time `json:"connected_at"`
			} `json:"tunnel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return &checkResult{name: "tunnel", ok: false, err: "failed to parse health response"}
	}

	if health.Data.Tunnel.Connected {
		detail := "connected"
		if health.Data.Tunnel.ConnectedAt != nil {
			detail = fmt.Sprintf("connected since %s", health.Data.Tunnel.ConnectedAt.Format(time.RFC3339))
		}
		return &checkResult{name: "tunnel", ok: true, detail: detail}
	}

	return &checkResult{
		name: "tunnel",
		err:  "tunnel not connected",
		hint: "The device may appear offline on the cloud. Check 'logs' for tunnel connection errors.",
	}
}

func resolveUser(cred *provider.Credentials) string {
	if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
		p := claims.ResolveProvider()
		user := claims.ResolveDisplayName()
		if p != "" && user != "" {
			return p + "/" + user
		}
		return user
	}
	return cred.ID
}

func resolveCloudUser(ctx context.Context, cfg *config.Config, cred *provider.Credentials) string {
	cc := cloud.NewClient(cfg)
	baseURL := cc.CloudBaseURL(cred.BaseURL)

	providerName := ""
	if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
		providerName = claims.ResolveProvider()
	}

	user, err := provider.GetCloudCurrentUser(ctx, cred.AccessToken, baseURL)
	if err != nil {
		return "user lookup failed: " + err.Error()
	}
	display := user.Name
	if display == "" {
		display = user.Username
	}
	if providerName != "" {
		display = providerName + "/" + display
	}
	if user.SubjectID != "" {
		return fmt.Sprintf("%s (Platform ID=%s)", display, user.SubjectID)
	}
	return display
}
