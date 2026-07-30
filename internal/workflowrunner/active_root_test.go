package workflowrunner

import (
	"testing"
)

func TestIsActiveEnvRoot(t *testing.T) {
	d := &Driver{running: map[string]*taskRecord{}}

	if d.isActiveEnvRoot("/a/b") {
		t.Fatal("expected false with empty running map")
	}
	if d.isActiveEnvRoot("") {
		t.Fatal("expected false for empty path")
	}

	d.running["t1"] = &taskRecord{taskRoot: "/a/b"}
	if !d.isActiveEnvRoot("/a/b") {
		t.Fatal("expected true for an active taskRoot")
	}
	if d.isActiveEnvRoot("/a/c") {
		t.Fatal("expected false for a non-active path")
	}

	// A second running task with a different root keeps the first active.
	d.running["t2"] = &taskRecord{taskRoot: "/x/y"}
	if !d.isActiveEnvRoot("/a/b") || !d.isActiveEnvRoot("/x/y") {
		t.Fatal("both roots should be active")
	}

	// Releasing the first task frees its root but not the second's.
	delete(d.running, "t1")
	if d.isActiveEnvRoot("/a/b") {
		t.Fatal("expected /a/b inactive after its task released")
	}
	if !d.isActiveEnvRoot("/x/y") {
		t.Fatal("expected /x/y still active")
	}
}
