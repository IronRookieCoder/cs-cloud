package platform

import (
	"os"
	"testing"
)

func TestGetenvCompat_PrimaryWins(t *testing.T) {
	t.Setenv("CS_BRIDGE_TEST_VAR", "primary")
	t.Setenv("CS_CLOUD_TEST_VAR", "legacy")

	if got := GetenvCompat("CS_BRIDGE_TEST_VAR", "CS_CLOUD_TEST_VAR"); got != "primary" {
		t.Fatalf("GetenvCompat = %q, want %q", got, "primary")
	}
}

func TestGetenvCompat_LegacyFallback(t *testing.T) {
	os.Unsetenv("CS_BRIDGE_TEST_VAR")
	t.Setenv("CS_CLOUD_TEST_VAR", "legacy")

	if got := GetenvCompat("CS_BRIDGE_TEST_VAR", "CS_CLOUD_TEST_VAR"); got != "legacy" {
		t.Fatalf("GetenvCompat = %q, want %q (legacy fallback)", got, "legacy")
	}
}

func TestGetenvCompat_PrimaryEmptyFallsBack(t *testing.T) {
	t.Setenv("CS_BRIDGE_TEST_VAR", "")
	t.Setenv("CS_CLOUD_TEST_VAR", "legacy")

	if got := GetenvCompat("CS_BRIDGE_TEST_VAR", "CS_CLOUD_TEST_VAR"); got != "legacy" {
		t.Fatalf("GetenvCompat = %q, want %q (empty primary should fall back)", got, "legacy")
	}
}

func TestGetenvCompat_BothEmpty(t *testing.T) {
	os.Unsetenv("CS_BRIDGE_TEST_VAR")
	os.Unsetenv("CS_CLOUD_TEST_VAR")

	if got := GetenvCompat("CS_BRIDGE_TEST_VAR", "CS_CLOUD_TEST_VAR"); got != "" {
		t.Fatalf("GetenvCompat = %q, want empty", got)
	}
}
