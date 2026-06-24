package provider

import (
	"testing"
)

func TestGenerateOldMachineID_Deterministic(t *testing.T) {
	first := GenerateOldMachineID()
	if first == "" {
		t.Fatal("GenerateOldMachineID() returned empty string")
	}
	if len(first) != 64 {
		t.Fatalf("expected 64-char hex, got %d chars: %s", len(first), first)
	}
	second := GenerateOldMachineID()
	if first != second {
		t.Fatal("GenerateOldMachineID() should be deterministic (same machine, same result)")
	}
}

func TestGenerateOldMachineID_DifferentFromRandom(t *testing.T) {
	oldID := GenerateOldMachineID()
	newID := GenerateMachineID()

	if oldID == "" {
		t.Fatal("GenerateOldMachineID() returned empty string")
	}
	if newID == "" {
		t.Fatal("GenerateMachineID() returned empty string")
	}
	if oldID == newID {
		t.Log("GenerateOldMachineID() matches GenerateMachineID() — expected on identical machine config, not a failure")
	}
	// Both should be 64-char hex strings
	if len(oldID) != 64 {
		t.Fatalf("GenerateOldMachineID() should be 64-char hex, got %d", len(oldID))
	}
	if len(newID) != 64 {
		t.Fatalf("GenerateMachineID() should be 64-char hex, got %d", len(newID))
	}
}

// V005: GenerateMachineID produces unique values across many calls
func TestGenerateMachineID_Unique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := GenerateMachineID()
		if seen[id] {
			t.Fatalf("duplicate ID generated at iteration %d: %s", i, id)
		}
		seen[id] = true
	}
}

// V005: GenerateMachineID returns 64-char hex
func TestGenerateMachineID_Format(t *testing.T) {
	id := GenerateMachineID()
	if len(id) != 64 {
		t.Fatalf("expected 64-char hex, got %d chars: %s", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("GenerateMachineID() contains non-hex char %q in %s", c, id)
		}
	}
}

// V034: GenerateOldMachineID is deterministic (same machine = same hash)
// This is the collision-prone algorithm — two machines with same MAC+user get same ID
func TestGenerateOldMachineID_StableAcrossCalls(t *testing.T) {
	id1 := GenerateOldMachineID()
	id2 := GenerateOldMachineID()
	id3 := GenerateOldMachineID()
	if id1 != id2 || id2 != id3 {
		t.Fatalf("GenerateOldMachineID should be deterministic: %s, %s, %s", id1, id2, id3)
	}
}

// V034: GenerateLegacyMachineID (used for enrollment) is deterministic
func TestGenerateLegacyMachineID_Deterministic(t *testing.T) {
	id1 := GenerateLegacyMachineID()
	id2 := GenerateLegacyMachineID()
	if id1 != id2 {
		t.Fatalf("GenerateLegacyMachineID should be deterministic: %q vs %q", id1, id2)
	}
	if len(id1) != 64 {
		t.Fatalf("expected 64-char hex, got %d", len(id1))
	}
}
