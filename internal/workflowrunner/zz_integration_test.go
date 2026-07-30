package workflowrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
)

// TestIntegration_SetupCSCPlugins_RealCSC drives the REAL csc binary through
// the production setupCSCPlugins path against the local marketplace mirror.
// Gated by CS_CLOUD_E2E=1 + CSC_BIN on PATH — a one-off local verification, not
// part of the normal suite. Run with:
//   CSC_CLOUD_E2E=1 go test ./internal/workflowrunner/ -run Integration_RealCSC -count=1 -v
func TestIntegration_SetupCSCPlugins_RealCSC(t *testing.T) {
	if os.Getenv("CSC_CLOUD_E2E") != "1" {
		t.Skip("set CSC_CLOUD_E2E=1 to run the real-csc integration test")
	}
	cscBin, err := exec.LookPath("csc")
	if err != nil {
		t.Skip("csc not on PATH")
	}
	workDir := t.TempDir()

	// Configurable so the test is not pinned to one machine. Defaults point at
	// the local marketplace mirror used for verification.
	marketplaceRepo := envDefault("CS_CLOUD_E2E_MARKETPLACE", "C:/Users/SXF-Admin/multica-marketplace-mirror")
	marketplaceName := envDefault("CS_CLOUD_E2E_MARKETPLACE_NAME", "costrict-plugins")
	pluginName := envDefault("CS_CLOUD_E2E_PLUGIN", "gitnexus")
	plugin := &workflow.PluginSpec{
		Name: pluginName,
		Install: &workflow.PluginInstallSpec{
			Method:          "plugin_marketplace",
			PluginName:      pluginName,
			MarketplaceName: marketplaceName,
			MarketplaceRepo: marketplaceRepo,
		},
	}
	if err := setupCSCPlugins(context.Background(), cscBin, workDir, plugin); err != nil {
		t.Fatalf("setupCSCPlugins with real csc failed: %v", err)
	}

	// Confirm the plugin landed in the workdir's local scope.
	list := exec.CommandContext(context.Background(), cscBin, "plugin", "list")
	list.Dir = workDir
	out, _ := list.CombinedOutput()
	t.Logf("csc plugin list (cwd=%s):\n%s", workDir, out)
	if !strings.Contains(string(out), "gitnexus") {
		t.Errorf("gitnexus not found in local plugin list after install")
	}

	// Also sanity-check installCloudSkill's command shape against real csc help
	// (no network install here — just that csc accepts the argv via --help parity,
	// already covered by unit tests; left as a log breadcrumb).
	t.Logf("workdir artifacts: %s", listDir(t, workDir))
}

func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err.Error()
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ", ") + " (root=" + filepath.Base(dir) + ")"
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
