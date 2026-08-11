package membertask

import "testing"

func TestParseTaskKeyRejectsTraversal(t *testing.T) {
	invalid := []string{
		"cloud/ws/../worker",
		"cloud/ws/node/owner",
		"cloud/ws/node/worker/extra",
		"cloud//node/worker",
		"cloud/ws/node/unknown",
		"cloud\\ws\\node\\worker",
	}
	for _, raw := range invalid {
		if _, err := ParseTaskKey(raw); err == nil {
			t.Errorf("ParseTaskKey(%q) succeeded, want error", raw)
		}
	}
}

func TestParseTaskKeyRoundTrip(t *testing.T) {
	key, err := ParseTaskKey("cloud-1/ws-2/node-3/worker")
	if err != nil {
		t.Fatalf("ParseTaskKey: %v", err)
	}
	if got := key.String(); got != "cloud-1/ws-2/node-3/worker" {
		t.Fatalf("String() = %q", got)
	}
}

func TestProjectStatusPrefersAcceptedOperationOverLostAuthority(t *testing.T) {
	p := ProjectStatus(Facts{AcceptedOperation: true, WriteAuthorityLost: true})
	if p.DisplayStatus != StatusSubmitting {
		t.Fatalf("status = %s", p.DisplayStatus)
	}
	if !p.Flags.WriteAuthorityLost {
		t.Fatal("missing write_authority_lost")
	}
	if len(p.AvailableActions) != 1 || p.AvailableActions[0] != "get" {
		t.Fatalf("actions = %#v", p.AvailableActions)
	}
}

func TestProjectStatusUsesFixedPriority(t *testing.T) {
	tests := []struct {
		name  string
		facts Facts
		want  DisplayStatus
	}{
		{"ended", Facts{Ended: true, AcceptedOperation: true}, StatusEnded},
		{"submitting", Facts{AcceptedOperation: true, ReadOnly: true}, StatusSubmitting},
		{"read only", Facts{ReadOnly: true, ReprepareRequired: true}, StatusReadOnly},
		{"reprepare", Facts{ReprepareRequired: true, ReconfirmationRequired: true}, StatusReprepareRequired},
		{"reconfirmation", Facts{ReconfirmationRequired: true, SyncPending: true}, StatusReconfirmationRequired},
		{"sync pending", Facts{SyncPending: true, ConfirmationWaiting: true}, StatusSyncPending},
		{"confirmation", Facts{ConfirmationWaiting: true, Activity: ActivityActive}, StatusConfirmationWaiting},
		{"active", Facts{Activity: ActivityActive, Prepared: true}, StatusInProgress},
		{"prepared", Facts{Prepared: true}, StatusPrepared},
		{"not prepared", Facts{}, StatusNotPrepared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectStatus(tt.facts).DisplayStatus; got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestProjectStatusUnknownRemoteStateIsReadOnly(t *testing.T) {
	p := ProjectStatus(Facts{RemoteState: "future_state", Prepared: true})
	if p.DisplayStatus != StatusReadOnly {
		t.Fatalf("status = %s", p.DisplayStatus)
	}
	if len(p.AvailableActions) != 1 || p.AvailableActions[0] != "get" {
		t.Fatalf("actions = %#v", p.AvailableActions)
	}
}
