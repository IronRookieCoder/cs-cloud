package workflow

import "testing"

func TestCacheReadWriteWorkspaces(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)

	wss := []Workspace{{ID: "ws-1", Name: "Test"}}
	if err := c.WriteWorkspaces(wss); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := c.ReadWorkspaces()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ws-1" {
		t.Fatalf("unexpected: %+v", got)
	}
}
