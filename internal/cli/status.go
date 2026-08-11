package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/provider"
)

// parseStatusJSONFlag reports whether `cs-cloud status --json` was requested.
// Accepts `--json`, `--json=true`, `--json=false` (the latter two are explicit).
func parseStatusJSONFlag() bool {
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "--json":
			return true
		case strings.HasPrefix(arg, "--json="):
			val := strings.TrimPrefix(arg, "--json=")
			return val == "true" || val == "1"
		}
	}
	return false
}

// statusJSONSchema is the wire contract for `cs-cloud status --json`.
// Fields are additive; consumers MUST tolerate new fields (semver-minor).
type statusJSONSchema struct {
	Running          bool   `json:"running"`
	Pid              int    `json:"pid,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Mode             string `json:"mode,omitempty"`
	Root             string `json:"root,omitempty"`
	CloudURL         string `json:"cloud_url,omitempty"`
	LocalURL         string `json:"local_url,omitempty"`
	Authenticated    bool   `json:"authenticated"`
	User             string `json:"user,omitempty"`
	Provider         string `json:"provider,omitempty"`
	DeviceID         string `json:"device_id,omitempty"`
	LegacyDeviceID   string `json:"legacy_device_id,omitempty"`
	// csc_serve_running must NOT use omitempty — clients rely on the
	// explicit false to distinguish "probed and not running" from "unknown".
	CSCServeRunning  bool   `json:"csc_serve_running"`
	// TunnelConnected must NOT use omitempty — same reason as CSCServeRunning.
	TunnelConnected  bool                  `json:"tunnel_connected"`
	TunnelConnectedAt *time.Time           `json:"tunnel_connected_at,omitempty"`
}

// tunnelProbeResult is the subset of /api/v1/runtime/health that we care about.
type tunnelProbeResult struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at"`
}

// statusJSON emits machine-readable status for csc TUI's cloudNotify
// bootstrap detection. All fields are best-effort: errors degrade to empty
// strings rather than failing the whole payload.
func statusJSON(a *app.App) error {
	running, pid, reason := a.DaemonStatus()

	out := statusJSONSchema{
		Running: running,
		Pid:     pid,
		Reason:  reason,
		Mode:    a.LoadMode(),
		Root:    a.RootDir(),
	}

	if cred, err := provider.LoadCredentials(); err == nil && cred != nil {
		out.Authenticated = true
		if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
			out.User = claims.ResolveDisplayName()
			out.Provider = claims.ResolveProvider()
		}
	}

	if dev, err := a.Device(); err == nil && dev != nil {
		out.DeviceID = dev.DeviceID
	} else {
		out.DeviceID = provider.GenerateMachineID()
	}
	out.LegacyDeviceID = device.GetLegacyDeviceID()

	if serverURL, err := a.ServerURL(); err == nil {
		out.LocalURL = serverURL
	}
	out.CloudURL = a.CloudBaseURL()

	// csc_serve_running is best-effort: probe the local daemon health endpoint.
	if running && out.LocalURL != "" {
		out.CSCServeRunning = probeCSCServeRunning(out.LocalURL)
		tunnel := probeTunnelStatus(out.LocalURL)
		out.TunnelConnected = tunnel.Connected
		out.TunnelConnectedAt = tunnel.ConnectedAt
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// probeCSCServeRunning pings the local daemon's agent health endpoint.
// Returns false on any error (daemon down, csc adapter not yet booted, etc.).
// Timeout is short to keep `status --json` snappy for csc TUI callers.
func probeCSCServeRunning(localURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localURL+"/api/v1/agents/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// probeTunnelStatus reads the daemon's /runtime/health endpoint for the
// current tunnel connectivity. Best-effort: returns Connected=false on any
// error (endpoint missing, daemon down, parse failure, etc.). Reuses the
// health endpoint rather than introducing a new one.
func probeTunnelStatus(localURL string) tunnelProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localURL+"/api/v1/runtime/health", nil)
	if err != nil {
		return tunnelProbeResult{}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tunnelProbeResult{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tunnelProbeResult{}
	}
	var envelope struct {
		Data struct {
			Tunnel *tunnelProbeResult `json:"tunnel"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return tunnelProbeResult{}
	}
	if envelope.Data.Tunnel == nil {
		return tunnelProbeResult{}
	}
	return *envelope.Data.Tunnel
}

func status(a *app.App) error {
	if parseStatusJSONFlag() {
		return statusJSON(a)
	}
	running, pid, reason := a.DaemonStatus()
	cred, err := provider.LoadCredentials()
	if err != nil {
		return err
	}
	dev, err := a.Device()
	if err != nil {
		return err
	}
	mode := a.LoadMode()
	serverURL, err := a.ServerURL()
	if err != nil {
		return err
	}

	printTitle("status")

	deviceIDVal := ""
	if dev != nil {
		deviceIDVal = dev.DeviceID
	} else {
		deviceIDVal = provider.GenerateMachineID()
	}

	if running {
		printSuccess("Running")
		printSection("Developer info")
		printKV("pid", fmt.Sprintf("%d", pid))
		printKV("mode", mode)
		printKV("root", a.RootDir())
		printKV("cloud_url", a.CloudBaseURL())
		printKV("auth", fmt.Sprintf("%t", cred != nil))
		if cred != nil {
			if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
				user := claims.ResolveDisplayName()
				p := claims.ResolveProvider()
				if p != "" || user != "" {
					printKV("user", p+"/"+user)
				}
			}
		}
		printKV("device", fmt.Sprintf("%t", dev != nil))
		printKV("device_id", deviceIDVal)
		printKV("legacy_device_id", device.GetLegacyDeviceID())
		p, m, h, u := provider.MachineIDParts()
		printKV("device_id.platform", p)
		printKV("device_id.mac", m)
		printKV("device_id.hostname", h)
		printKV("device_id.username", u)
		printKV("local_url", serverURL)
		printKV("logs", filepath.Join(a.RootDir(), "app.log"))
		tunnel := probeTunnelStatus(serverURL)
		if tunnel.Connected {
			printKV("tunnel", "connected")
			if tunnel.ConnectedAt != nil {
				printKV("tunnel_since", tunnel.ConnectedAt.Format(time.RFC3339))
			}
		} else {
			printKV("tunnel", "disconnected")
		}
		printAgentRuntimes(serverURL)

		if mode == "cloud" {
			webURL := strings.TrimSuffix(a.CloudBaseURL(), "/cloud-api") + "/cloud"
			fmt.Println()
			fmt.Println(headingStyle.Render("→ Cloud dashboard"))
			fmt.Printf("  %s\n", valueStyle.Render(webURL))
		}
	} else {
		if reason != "" {
			printWarn("Stopped (%s)", reason)
		} else {
			printInfo("Stopped")
		}
		printSection("Developer info")
		printKV("root", a.RootDir())
		printKV("cloud_url", a.CloudBaseURL())
		printKV("auth", fmt.Sprintf("%t", cred != nil))
		if cred != nil {
			if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
				user := claims.ResolveDisplayName()
				p := claims.ResolveProvider()
				if p != "" || user != "" {
					printKV("user", p+"/"+user)
				}
			}
		}
		printKV("device", fmt.Sprintf("%t", dev != nil))
		printKV("device_id", deviceIDVal)
		printKV("legacy_device_id", device.GetLegacyDeviceID())
	}
	return nil
}

func printAgentRuntimes(serverURL string) {
	if serverURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/v1/agents/health", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var envelope struct {
		Data struct {
			Agents []struct {
				ID         string `json:"id"`
				Backend    string `json:"backend"`
				Driver     string `json:"driver"`
				State      string `json:"state"`
				Available  bool   `json:"available"`
				LatencyMs  int64  `json:"latency_ms"`
				Error      string `json:"error"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return
	}

	agents := envelope.Data.Agents
	if len(agents) == 0 {
		return
	}

	printSection("Agent runtimes")
	for _, ag := range agents {
		status := ag.State
		if ag.Available {
			status = "healthy"
		} else if ag.Error != "" {
			status = "unhealthy (" + ag.Error + ")"
		}
		printKV("agent", fmt.Sprintf("%s [%s] %s", ag.Backend, ag.ID, status))
		if ag.LatencyMs > 0 {
			printKV("latency", fmt.Sprintf("%dms", ag.LatencyMs))
		}
	}
}
