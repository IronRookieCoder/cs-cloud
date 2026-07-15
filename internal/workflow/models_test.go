package workflow

import "testing"

func TestTaskStatusString(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskStatusPending, "pending"},
		{TaskStatusRunning, "running"},
		{TaskStatusComplete, "complete"},
		{TaskStatusFailed, "failed"},
		{TaskStatusAborted, "aborted"},
		{TaskStatus(99), "unknown"},
	}
	for _, tc := range tests {
		got := tc.status.String()
		if got != tc.want {
			t.Fatalf("%v.String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}
