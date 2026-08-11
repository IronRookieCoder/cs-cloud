package membertask

import (
	"fmt"
	"regexp"
	"strings"
)

const SchemaVersion = "1.0"

type Role string

const (
	RoleWorker Role = "worker"
	RoleCritic Role = "critic"
)

type TaskKey struct {
	CloudInstanceID string `json:"cloud_instance_id"`
	WorkspaceID     string `json:"workspace_id"`
	NodeRunID       string `json:"node_run_id"`
	Role            Role   `json:"role"`
}

var taskKeyPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ParseTaskKey(raw string) (TaskKey, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 4 {
		return TaskKey{}, newTaskError("invalid_task_key", "task key must have four segments", nil)
	}
	for _, part := range parts[:3] {
		if part == "." || part == ".." || !taskKeyPartPattern.MatchString(part) {
			return TaskKey{}, newTaskError("invalid_task_key", "task key contains an invalid segment", nil)
		}
	}
	role := Role(parts[3])
	if role != RoleWorker && role != RoleCritic {
		return TaskKey{}, newTaskError("invalid_task_key", "task role must be worker or critic", nil)
	}
	return TaskKey{CloudInstanceID: parts[0], WorkspaceID: parts[1], NodeRunID: parts[2], Role: role}, nil
}

func (k TaskKey) String() string {
	return strings.Join([]string{k.CloudInstanceID, k.WorkspaceID, k.NodeRunID, string(k.Role)}, "/")
}

type DisplayStatus string

const (
	StatusEnded                  DisplayStatus = "ended"
	StatusSubmitting             DisplayStatus = "submitting"
	StatusReadOnly               DisplayStatus = "read_only"
	StatusReprepareRequired      DisplayStatus = "reprepare_required"
	StatusReconfirmationRequired DisplayStatus = "reconfirmation_required"
	StatusSyncPending            DisplayStatus = "sync_pending"
	StatusConfirmationWaiting    DisplayStatus = "confirmation_waiting"
	StatusInProgress             DisplayStatus = "in_progress"
	StatusPrepared               DisplayStatus = "prepared"
	StatusNotPrepared            DisplayStatus = "not_prepared"
)

type Activity string

const (
	ActivityPrepared Activity = "prepared"
	ActivityActive   Activity = "active"
	ActivityPaused   Activity = "paused"
)

type Flags struct {
	Offline                   bool `json:"offline"`
	Dirty                     bool `json:"dirty"`
	CloudStateUnverified      bool `json:"cloud_state_unverified"`
	OperationRecoveryRequired bool `json:"operation_recovery_required"`
	WonByOtherOperation       bool `json:"won_by_other_operation"`
	WriteAuthorityLost        bool `json:"write_authority_lost"`
	CleanupPending            bool `json:"cleanup_pending"`
}

type Facts struct {
	Ended                  bool
	AcceptedOperation      bool
	ReadOnly               bool
	ReprepareRequired      bool
	ReconfirmationRequired bool
	SyncPending            bool
	ConfirmationWaiting    bool
	Prepared               bool
	Activity               Activity
	Role                   Role
	RemoteState            string
	Flags                  Flags
	WriteAuthorityLost     bool
}

type Projection struct {
	DisplayStatus    DisplayStatus `json:"display_status"`
	Flags            Flags         `json:"flags"`
	AvailableActions []string      `json:"available_actions"`
}

func ProjectStatus(f Facts) Projection {
	f.Flags.WriteAuthorityLost = f.Flags.WriteAuthorityLost || f.WriteAuthorityLost
	if f.RemoteState != "" && !knownRemoteState(f.RemoteState) {
		f.ReadOnly = true
	}

	status := StatusNotPrepared
	switch {
	case f.Ended:
		status = StatusEnded
	case f.AcceptedOperation:
		status = StatusSubmitting
	case f.ReadOnly || f.Flags.WonByOtherOperation:
		status = StatusReadOnly
	case f.ReprepareRequired:
		status = StatusReprepareRequired
	case f.ReconfirmationRequired:
		status = StatusReconfirmationRequired
	case f.SyncPending:
		status = StatusSyncPending
	case f.ConfirmationWaiting:
		status = StatusConfirmationWaiting
	case f.Activity == ActivityActive || f.Activity == ActivityPaused:
		status = StatusInProgress
	case f.Prepared:
		status = StatusPrepared
	}

	return Projection{DisplayStatus: status, Flags: f.Flags, AvailableActions: availableActions(status, f.Role, f.Flags)}
}

func knownRemoteState(state string) bool {
	switch state {
	case "assigned", "prepared", "in_progress", "rework", "submitting", "ended":
		return true
	default:
		return false
	}
}

func availableActions(status DisplayStatus, role Role, flags Flags) []string {
	switch status {
	case StatusEnded:
		return []string{"get", "delete"}
	case StatusSubmitting, StatusSyncPending:
		return []string{"get"}
	case StatusReadOnly:
		return []string{"get"}
	case StatusReprepareRequired:
		return []string{"get", "prepare", "delete"}
	case StatusReconfirmationRequired, StatusConfirmationWaiting:
		return []string{"get", terminalAction(role), "delete"}
	case StatusInProgress:
		return []string{"get", "pause", terminalAction(role), "delete"}
	case StatusPrepared:
		return []string{"get", "start", "delete"}
	default:
		return []string{"get", "prepare"}
	}
}

func terminalAction(role Role) string {
	if role == RoleCritic {
		return "review"
	}
	return "submit"
}

type Outcome string

const (
	OutcomeObserved         Outcome = "observed"
	OutcomeCompleted        Outcome = "completed"
	OutcomeAlreadyCompleted Outcome = "already_completed"
	OutcomePreviewed        Outcome = "previewed"
	OutcomeRecovered        Outcome = "recovered"
	OutcomeNotCompleted     Outcome = "not_completed"
)

type TaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   string `json:"cause,omitempty"`
	err     error
}

func (e *TaskError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *TaskError) Unwrap() error { return e.err }

func newTaskError(code, message string, err error) *TaskError {
	te := &TaskError{Code: code, Message: message, err: err}
	if err != nil {
		te.Cause = err.Error()
	}
	return te
}
