package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/provider"
)

func status(a *app.App) error {
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
		p, m, u := provider.MachineIDParts()
		printKV("device_id.platform", p)
		printKV("device_id.mac", m)
		printKV("device_id.username", u)
		printKV("local_url", serverURL)
		printKV("logs", filepath.Join(a.RootDir(), "app.log"))
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
