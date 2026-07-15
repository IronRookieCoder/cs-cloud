package workflow

import "testing"

func TestTaskStatusString(t *testing.T) {
	if TaskStatusRunning.String() != "running" {
		t.Fatal("unexpected status string")
	}
}
