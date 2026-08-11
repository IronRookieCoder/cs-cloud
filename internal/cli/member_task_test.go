package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"cs-cloud/internal/membertask"
)

type fakeMemberTaskAPI struct {
	calls    []memberTaskRequest
	response any
	err      error
}

func (f *fakeMemberTaskAPI) Execute(ctx context.Context, request memberTaskRequest) (any, error) {
	f.calls = append(f.calls, request)
	return f.response, f.err
}

func TestTaskHelpCatalogDescribesEveryPublicCommand(t *testing.T) {
	catalog := memberTaskHelpCatalog()
	want := []string{"help", "list", "get", "prepare", "start", "pause", "submit", "review", "delete"}
	if len(catalog.Commands) != len(want) {
		t.Fatalf("commands = %+v", catalog.Commands)
	}
	for i, command := range catalog.Commands {
		if command.Name != want[i] || command.Summary == "" || command.Capability == "" || command.TimeoutSeconds <= 0 || command.ConfirmationMode == "" || len(command.Outcomes) == 0 {
			t.Fatalf("command[%d] = %+v", i, command)
		}
	}
}

func TestTaskHelpCatalogDescribesHowToConstructCommandArguments(t *testing.T) {
	catalog := memberTaskHelpCatalog()
	commands := make(map[string]CommandSpec, len(catalog.Commands))
	for _, command := range catalog.Commands {
		commands[command.Name] = command
	}

	wantTaskKey := ArgumentSpec{
		Name: "task_key", Kind: "positional", Type: "string", Required: true,
		Summary: "Stable cloud/workspace/node/role task identity",
	}
	wantConfirm := ArgumentSpec{
		Name: "confirm", Kind: "flag", Flag: "--confirm", Type: "string",
		Summary: "Previously issued preview id",
	}
	if got := commands["submit"].Arguments; !reflect.DeepEqual(got, []ArgumentSpec{wantTaskKey, wantConfirm}) {
		t.Fatalf("submit arguments = %+v", got)
	}

	wantDecision := ArgumentSpec{
		Name: "decision", Kind: "flag", Flag: "--decision", Type: "enum",
		Enum: []string{"approve", "reject"}, RequiredUnless: "confirm",
		Summary: "Critic decision",
	}
	wantReason := ArgumentSpec{
		Name: "reason", Kind: "flag", Flag: "--reason", Type: "string",
		RequiredWhen: &ArgumentCondition{Argument: "decision", Equals: "reject"},
		Summary:      "Required for reject",
	}
	review := commands["review"]
	if !reflect.DeepEqual(review.Arguments, []ArgumentSpec{wantTaskKey, wantDecision, wantReason, wantConfirm}) {
		t.Fatalf("review arguments = %+v", review.Arguments)
	}
	if got := review.MutuallyExclusive; len(got) != 2 || len(got[0]) != 2 || got[0][0] != "confirm" || got[0][1] != "decision" || len(got[1]) != 2 || got[1][0] != "confirm" || got[1][1] != "reason" {
		t.Fatalf("review mutually_exclusive = %+v", got)
	}

	wantForceDiscard := ArgumentSpec{
		Name: "force_discard", Kind: "flag", Flag: "--force-discard", Type: "boolean",
		Summary: "Preview explicit risky discard",
	}
	deleteCommand := commands["delete"]
	if !reflect.DeepEqual(deleteCommand.Arguments, []ArgumentSpec{wantTaskKey, wantForceDiscard, wantConfirm}) {
		t.Fatalf("delete arguments = %+v", deleteCommand.Arguments)
	}
	if got := deleteCommand.MutuallyExclusive; len(got) != 1 || len(got[0]) != 2 || got[0][0] != "confirm" || got[0][1] != "force_discard" {
		t.Fatalf("delete mutually_exclusive = %+v", got)
	}
}

func TestMemberTaskRejectsReviewWithoutReasonBeforeTransport(t *testing.T) {
	api := &fakeMemberTaskAPI{}
	var stdout, stderr bytes.Buffer
	err := runMemberTaskCommand(context.Background(), []string{"review", "cloud/ws/node/critic", "--decision", "reject"}, api, &stdout, &stderr)
	if exitCode(err) != 2 {
		t.Fatalf("exit code = %d, err=%v", exitCode(err), err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("transport calls = %+v", api.calls)
	}
	var envelope taskCommandEnvelope
	if json.Unmarshal(stdout.Bytes(), &envelope) != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_arguments" {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("next: none")) {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestMemberTaskRejectsConfirmMixedWithPreviewArguments(t *testing.T) {
	api := &fakeMemberTaskAPI{}
	var stdout, stderr bytes.Buffer
	err := runMemberTaskCommand(context.Background(), []string{"review", "cloud/ws/node/critic", "--confirm", "preview-1", "--decision", "approve"}, api, &stdout, &stderr)
	if exitCode(err) != 2 || len(api.calls) != 0 {
		t.Fatalf("err=%v calls=%+v", err, api.calls)
	}
}

func TestMemberTaskListWritesVersionedEnvelope(t *testing.T) {
	api := &fakeMemberTaskAPI{response: []membertask.Task{}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"list"}, api, &stdout, &stderr); err != nil {
		t.Fatalf("runMemberTaskCommand: %v", err)
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode stdout: %v: %s", err, stdout.String())
	}
	if !envelope.OK || envelope.SchemaVersion != membertask.SchemaVersion || envelope.Command != "task list" || envelope.Outcome != membertask.OutcomeObserved || envelope.ObservedAt.IsZero() {
		t.Fatalf("envelope = %+v", envelope)
	}
	if len(api.calls) != 1 || api.calls[0].Command != "list" {
		t.Fatalf("calls = %+v", api.calls)
	}
}

func TestTaskEnvelopeMapsBusinessRejectionToExitFour(t *testing.T) {
	api := &fakeMemberTaskAPI{err: &membertask.TaskError{Code: "provider_not_supported", Message: "unsupported"}}
	var stdout, stderr bytes.Buffer
	err := runMemberTaskCommand(context.Background(), []string{"prepare", "cloud/ws/node/worker"}, api, &stdout, &stderr)
	if exitCode(err) != 4 {
		t.Fatalf("exit code = %d, err=%v", exitCode(err), err)
	}
	if !errors.Is(err, errTaskCommandReported) {
		t.Fatalf("error is not marked reported: %v", err)
	}
	if bytes.Contains(stdout.Bytes(), []byte("unsupported")) && bytes.Contains(stdout.Bytes(), []byte("secret")) {
		t.Fatalf("unexpected unsafe output: %s", stdout.String())
	}
}

func TestTaskEnvelopeMapsPreparationCapabilityErrorsToExitFour(t *testing.T) {
	for _, code := range []string{"material_content_unavailable", "repository_access_unavailable"} {
		t.Run(code, func(t *testing.T) {
			api := &fakeMemberTaskAPI{err: &membertask.TaskError{Code: code, Message: "capability unavailable"}}
			var stdout, stderr bytes.Buffer
			err := runMemberTaskCommand(context.Background(), []string{"prepare", "cloud/ws/node/worker"}, api, &stdout, &stderr)
			if exitCode(err) != 4 {
				t.Fatalf("exit code = %d, err=%v", exitCode(err), err)
			}
		})
	}
}

func TestTaskEnvelopeIncludesStateAfterForLocalTransition(t *testing.T) {
	key, _ := membertask.ParseTaskKey("cloud/ws/node/worker")
	api := &fakeMemberTaskAPI{response: membertask.TaskRecord{Key: key, Prepared: true, Activity: membertask.ActivityActive}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"start", key.String()}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &envelope)
	if envelope.StateAfter == nil || envelope.StateAfter.DisplayStatus != membertask.StatusInProgress {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestTaskEnvelopeUsesOperationOutcome(t *testing.T) {
	api := &fakeMemberTaskAPI{response: membertask.Operation{ID: "operation-1", Status: membertask.OperationCompleted, Outcome: membertask.OutcomeRecovered}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker", "--confirm", "preview-1"}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &envelope)
	if envelope.Outcome != membertask.OutcomeRecovered {
		t.Fatalf("outcome = %s", envelope.Outcome)
	}
}
