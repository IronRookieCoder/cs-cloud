package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
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
	catalog := decodeTaskHelpCatalog(t)
	want := []string{"help", "list", "get", "handle", "submit", "review", "delete", "recover"}
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
	catalog := decodeTaskHelpCatalog(t)
	commands := make(map[string]CommandSpec, len(catalog.Commands))
	for _, command := range catalog.Commands {
		commands[command.Name] = command
	}

	wantTaskKey := ArgumentSpec{
		Name: "task_key", Kind: "positional", Type: "string", Required: true,
		Summary: "Stable cloud/workspace/node/role task identity",
	}
	wantConfirm := ArgumentSpec{
		Name: "confirm", Kind: "flag", Flag: "--confirm", Type: "string", ValueStyle: "equals_or_unprefixed_separate",
		Summary: "Previously issued preview id",
	}
	wantWorkDir := ArgumentSpec{
		Name: "workdir", Kind: "flag", Flag: "--workdir", Type: "string", ValueStyle: "equals_or_unprefixed_separate",
		Summary: "Existing working directory that will contain .cs-cloud-tasks",
	}
	wantDeliverable := ArgumentSpec{Name: "deliverable", Kind: "flag", Flag: "--deliverable", Type: "string", ValueStyle: "repeatable_pair", Summary: "Deliverable id paired with the following --file"}
	wantFile := ArgumentSpec{Name: "file", Kind: "flag", Flag: "--file", Type: "string", ValueStyle: "repeatable_pair", Summary: "Prepared task file paired with the preceding --deliverable"}
	if got := commands["handle"].Arguments; !reflect.DeepEqual(got, []ArgumentSpec{wantTaskKey, wantWorkDir}) {
		t.Fatalf("handle arguments = %+v", got)
	}
	if got := commands["submit"].Arguments; !reflect.DeepEqual(got, []ArgumentSpec{wantTaskKey, wantDeliverable, wantFile, wantConfirm}) {
		t.Fatalf("submit arguments = %+v", got)
	}
	if got := commands["submit"].MutuallyExclusive; !reflect.DeepEqual(got, [][]string{{"confirm", "deliverable"}, {"confirm", "file"}}) {
		t.Fatalf("submit mutually_exclusive = %+v", got)
	}

	wantDecision := ArgumentSpec{
		Name: "decision", Kind: "flag", Flag: "--decision", Type: "enum", ValueStyle: "equals_or_unprefixed_separate",
		Enum: []string{"approve", "reject"}, RequiredUnless: "confirm",
		Summary: "Critic decision",
	}
	wantReason := ArgumentSpec{
		Name: "reason", Kind: "flag", Flag: "--reason", Type: "string", ValueStyle: "equals_or_unprefixed_separate",
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

func TestMemberTaskSubmitParsesOrderedDeliverableFileBindings(t *testing.T) {
	request, err := parseMemberTaskRequest([]string{
		"submit", "cloud/ws/node/worker",
		"--deliverable", "first", "--file", "output/first.md",
		"--deliverable=second", "--file=output/second.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []membertask.DeliverableFileBinding{{DeliverableID: "first", File: "output/first.md"}, {DeliverableID: "second", File: "output/second.md"}}
	if !reflect.DeepEqual(request.DeliverableFiles, want) {
		t.Fatalf("bindings = %#v, want %#v", request.DeliverableFiles, want)
	}
}

func TestMemberTaskSubmitRejectsInvalidBindingFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing file", args: []string{"submit", "cloud/ws/node/worker", "--deliverable", "first"}},
		{name: "missing deliverable", args: []string{"submit", "cloud/ws/node/worker", "--file", "output.md"}},
		{name: "repeated deliverable", args: []string{"submit", "cloud/ws/node/worker", "--deliverable", "first", "--deliverable", "second", "--file", "output.md"}},
		{name: "repeated file", args: []string{"submit", "cloud/ws/node/worker", "--deliverable", "first", "--file", "a.md", "--file", "b.md"}},
		{name: "confirm mixed", args: []string{"submit", "cloud/ws/node/worker", "--deliverable", "first", "--file", "output.md", "--confirm", "preview-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseMemberTaskRequest(test.args); err == nil {
				t.Fatal("request accepted")
			}
		})
	}
}

func TestTaskHelpCatalogDescribesDeliverableFileBindings(t *testing.T) {
	catalog := decodeTaskHelpCatalog(t)
	for _, command := range catalog.Commands {
		if command.Name != "submit" {
			continue
		}
		flags := map[string]ArgumentSpec{}
		for _, argument := range command.Arguments {
			flags[argument.Flag] = argument
		}
		if flags["--deliverable"].Summary == "" || flags["--file"].Summary == "" || flags["--deliverable"].ValueStyle != "repeatable_pair" || flags["--file"].ValueStyle != "repeatable_pair" {
			t.Fatalf("submit arguments = %#v", command.Arguments)
		}
		return
	}
	t.Fatal("submit command missing")
}

func TestHandleRequestAcceptsWorkDirAndSendsItToLocalService(t *testing.T) {
	request, err := parseMemberTaskRequest([]string{"handle", "cloud/ws/node/worker", "--workdir=./project"})
	if err != nil {
		t.Fatal(err)
	}
	if request.WorkDir != "./project" {
		t.Fatalf("workdir = %q", request.WorkDir)
	}
	method, endpoint, body, err := memberTaskHTTPRequest(request)
	if err != nil || method != "POST" || endpoint == "" {
		t.Fatalf("method=%q endpoint=%q err=%v", method, endpoint, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload["workdir"] != "./project" {
		t.Fatalf("payload=%s err=%v", body, err)
	}
}

func TestRemovedTaskCommandsAreRejected(t *testing.T) {
	for _, command := range []string{"prepare", "start", "pause"} {
		if _, err := parseMemberTaskRequest([]string{command, "cloud/ws/node/worker"}); err == nil {
			t.Fatalf("%s was accepted", command)
		}
	}
}

func TestRecoverRequestUsesTaskOperationEndpoint(t *testing.T) {
	request, err := parseMemberTaskRequest([]string{"recover", "cloud/ws/node/worker"})
	if err != nil {
		t.Fatal(err)
	}
	method, endpoint, body, err := memberTaskHTTPRequest(request)
	if err != nil || method != "POST" || !strings.HasSuffix(endpoint, "/recover") || string(body) != "{}" {
		t.Fatalf("method=%q endpoint=%q body=%s err=%v", method, endpoint, body, err)
	}
}

func TestDeleteRequestRejectsInvalidBooleanFlagValue(t *testing.T) {
	_, err := parseMemberTaskRequest([]string{"delete", "cloud/ws/node/worker", "--force-discard=garbage"})
	var taskErr *membertask.TaskError
	if !errors.As(err, &taskErr) || taskErr.Code != "invalid_arguments" {
		t.Fatalf("parseMemberTaskRequest error = %#v", err)
	}
}

func TestWriteTaskTextRejectsMissingCommand(t *testing.T) {
	var out bytes.Buffer
	if err := writeTaskText(&out, &taskCommandData{}); err == nil {
		t.Fatal("writeTaskText accepted missing command")
	}
}

func TestWriteTaskTextIncludesLocalTransitionTaskDetails(t *testing.T) {
	key, _ := membertask.ParseTaskKey("cloud/ws/node/worker")
	var out bytes.Buffer
	data := &taskCommandData{Command: []string{"cs-cloud", "task", "handle"}, Result: membertask.LocalTransition{TaskRecord: membertask.TaskRecord{Key: key, DisplayName: "示例任务", Directory: `C:\tasks\node`}}}
	if err := writeTaskText(&out, data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "task: 示例任务\n") || !strings.Contains(out.String(), "directory: C:\\tasks\\node\n") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDisplayStatusTextIncludesSubmittingAndSyncPending(t *testing.T) {
	if got := displayStatusText(membertask.StatusSubmitting); got != "提交中" {
		t.Fatalf("submitting = %q", got)
	}
	if got := displayStatusText(membertask.StatusSyncPending); got != "等待同步" {
		t.Fatalf("sync pending = %q", got)
	}
}

func TestHandleCommandResolvesWorkDirBeforeTransport(t *testing.T) {
	key, _ := membertask.ParseTaskKey("cloud/ws/node/worker")
	api := &fakeMemberTaskAPI{response: membertask.LocalTransition{TaskRecord: membertask.TaskRecord{Key: key}}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"handle", key.String(), "--workdir=."}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 1 || !filepath.IsAbs(api.calls[0].WorkDir) {
		t.Fatalf("calls = %+v", api.calls)
	}
}

func TestTaskHelpCatalogIncludesFailureOutcome(t *testing.T) {
	for _, command := range decodeTaskHelpCatalog(t).Commands {
		found := false
		for _, outcome := range command.Outcomes {
			if outcome == membertask.OutcomeNotCompleted {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("command %q outcomes = %+v", command.Name, command.Outcomes)
		}
	}
}

func TestTaskHelpCatalogDeclaresRepeatedHandleOutcome(t *testing.T) {
	for _, command := range decodeTaskHelpCatalog(t).Commands {
		if command.Name != "handle" {
			continue
		}
		for _, outcome := range command.Outcomes {
			if outcome == membertask.OutcomeAlreadyCompleted {
				return
			}
		}
		t.Fatalf("handle outcomes = %+v", command.Outcomes)
	}
	t.Fatal("handle command is missing")
}

func decodeTaskHelpCatalog(t *testing.T) taskHelpCatalog {
	t.Helper()
	var output bytes.Buffer
	if err := printMemberTaskHelp(&output, true); err != nil {
		t.Fatalf("print task help: %v", err)
	}
	var catalog taskHelpCatalog
	if err := json.Unmarshal(output.Bytes(), &catalog); err != nil {
		t.Fatalf("decode task help: %v: %s", err, output.String())
	}
	return catalog
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

func TestReviewStaleErrorDoesNotSuggestIncompletePreviewArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	key := "cloud/ws/node/critic"
	err := reportTaskCommandError(&stdout, &stderr, []string{"review", key, "--confirm", "preview-1"}, &key, 4, &membertask.TaskError{Code: "review_snapshot_stale", Message: "stale"}, true)
	if exitCode(err) != 4 {
		t.Fatalf("exit code = %d", exitCode(err))
	}
	var envelope taskCommandEnvelope
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &envelope); unmarshalErr != nil {
		t.Fatalf("decode envelope: %v: %s", unmarshalErr, stdout.String())
	}
	if len(envelope.NextCommands) != 0 {
		t.Fatalf("next_commands = %+v", envelope.NextCommands)
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
	err := runMemberTaskCommand(context.Background(), []string{"handle", "cloud/ws/node/worker"}, api, &stdout, &stderr)
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
			err := runMemberTaskCommand(context.Background(), []string{"handle", "cloud/ws/node/worker"}, api, &stdout, &stderr)
			if exitCode(err) != 4 {
				t.Fatalf("exit code = %d, err=%v", exitCode(err), err)
			}
		})
	}
}

func TestTaskEnvelopeIncludesStateAfterForLocalTransition(t *testing.T) {
	key, _ := membertask.ParseTaskKey("cloud/ws/node/worker")
	api := &fakeMemberTaskAPI{response: membertask.TaskRecord{Key: key, Prepared: true}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"handle", key.String()}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &envelope)
	if envelope.StateAfter == nil || envelope.StateAfter.DisplayStatus != membertask.StatusReady {
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
	if envelope.Outcome != membertask.OutcomeRecovered || envelope.Performed {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestTaskEnvelopeDoesNotReportIncompleteOperationAsPerformed(t *testing.T) {
	for _, status := range []membertask.OperationStatus{membertask.OperationAccepted, membertask.OperationRunning} {
		t.Run(string(status), func(t *testing.T) {
			api := &fakeMemberTaskAPI{response: membertask.Operation{ID: "operation-1", Status: status}}
			var stdout, stderr bytes.Buffer
			if err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker", "--confirm", "preview-1"}, api, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			var envelope taskCommandEnvelope
			_ = json.Unmarshal(stdout.Bytes(), &envelope)
			if envelope.Outcome != membertask.OutcomeNotCompleted || envelope.Performed {
				t.Fatalf("envelope = %+v", envelope)
			}
		})
	}
}

func TestTaskEnvelopeUsesIdempotentLocalTransitionFacts(t *testing.T) {
	key, _ := membertask.ParseTaskKey("cloud/ws/node/worker")
	api := &fakeMemberTaskAPI{response: membertask.LocalTransition{
		TaskRecord: membertask.TaskRecord{Key: key, Prepared: true},
		Outcome:    membertask.OutcomeAlreadyCompleted,
		Performed:  false,
	}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"handle", key.String()}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &envelope)
	if envelope.Outcome != membertask.OutcomeAlreadyCompleted || envelope.Performed {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestTaskEnvelopeDoesNotReportNonTerminalOperationAsPerformed(t *testing.T) {
	statuses := []membertask.OperationStatus{
		membertask.OperationAccepted,
		membertask.OperationRunning,
		membertask.OperationRepreviewRequired,
		membertask.OperationConflict,
		membertask.OperationUnknown,
		membertask.OperationFailed,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			api := &fakeMemberTaskAPI{response: membertask.Operation{ID: "operation-1", Status: status}}
			var stdout, stderr bytes.Buffer
			if err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker", "--confirm", "preview-1"}, api, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			var envelope taskCommandEnvelope
			_ = json.Unmarshal(stdout.Bytes(), &envelope)
			if envelope.Outcome != membertask.OutcomeNotCompleted || envelope.Performed {
				t.Fatalf("envelope = %+v", envelope)
			}
		})
	}
}

func TestTaskEnvelopeDoesNotReportAlreadyCompletedAsPerformed(t *testing.T) {
	api := &fakeMemberTaskAPI{response: membertask.Operation{ID: "operation-1", Status: membertask.OperationCompleted, Outcome: membertask.OutcomeAlreadyCompleted}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker", "--confirm", "preview-1"}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &envelope)
	if envelope.Outcome != membertask.OutcomeAlreadyCompleted || envelope.Performed {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestTaskEnvelopeUsesFourFieldOuterContract(t *testing.T) {
	api := &fakeMemberTaskAPI{response: []membertask.Task{}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"list"}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 4 || raw["schema_version"] == nil || raw["ok"] == nil || raw["data"] == nil || raw["error"] == nil {
		t.Fatalf("outer envelope = %s", stdout.String())
	}
	var data struct {
		Command []string `json:"command"`
	}
	if err := json.Unmarshal(raw["data"], &data); err != nil || !reflect.DeepEqual(data.Command, []string{"cs-cloud", "task", "list"}) {
		t.Fatalf("data = %s, err = %v", raw["data"], err)
	}
}

func TestTaskEnvelopePreviewIncludesBoundConfirmationCommand(t *testing.T) {
	api := &fakeMemberTaskAPI{response: membertask.Preview{ID: "preview-1"}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker"}, api, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data == nil || len(envelope.Data.NextCommands) != 1 || !reflect.DeepEqual(envelope.Data.NextCommands[0].Argv, []string{"cs-cloud", "task", "submit", "cloud/ws/node/worker", "--confirm", "preview-1"}) {
		t.Fatalf("envelope = %s", stdout.String())
	}
}

func TestTaskEnvelopeFailureUsesOuterErrorAndFixedStaleSuggestion(t *testing.T) {
	api := &fakeMemberTaskAPI{err: &membertask.TaskError{Code: "preview_stale", Message: "stale"}}
	var stdout, stderr bytes.Buffer
	err := runMemberTaskCommand(context.Background(), []string{"submit", "cloud/ws/node/worker", "--confirm", "old-preview"}, api, &stdout, &stderr)
	if exitCode(err) != 4 {
		t.Fatalf("exit code = %d", exitCode(err))
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "preview_stale" || envelope.Data == nil || len(envelope.Data.NextCommands) != 1 {
		t.Fatalf("envelope = %s", stdout.String())
	}
	want := []string{"cs-cloud", "task", "submit", "cloud/ws/node/worker"}
	if envelope.Data.NextCommands[0].Safety != "preview_only" || !reflect.DeepEqual(envelope.Data.NextCommands[0].Argv, want) {
		t.Fatalf("next commands = %+v", envelope.Data.NextCommands)
	}
}

func TestTaskEnvelopeForceDiscardRequiredSuggestsOnlyForcePreview(t *testing.T) {
	api := &fakeMemberTaskAPI{err: &membertask.TaskError{Code: "force_discard_required", Message: "risky local state"}}
	var stdout, stderr bytes.Buffer
	key := "cloud/ws/node/worker"
	err := runMemberTaskCommand(context.Background(), []string{"delete", key}, api, &stdout, &stderr)
	if exitCode(err) != 4 {
		t.Fatalf("exit code = %d", exitCode(err))
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	want := []string{"cs-cloud", "task", "delete", key, "--force-discard"}
	if envelope.Data == nil || len(envelope.Data.NextCommands) != 1 || envelope.Data.NextCommands[0].Safety != "preview_only" || !reflect.DeepEqual(envelope.Data.NextCommands[0].Argv, want) {
		t.Fatalf("next commands = %+v, want one force-discard preview", envelope.Data)
	}
	if !bytes.Contains(stderr.Bytes(), []byte(strings.Join(want, " "))) || bytes.Contains(stderr.Bytes(), []byte("next: none")) {
		t.Fatalf("stderr = %q, want envelope next command", stderr.String())
	}
}

func TestTaskHelpCanDescribeOneCommandAndRejectUnknown(t *testing.T) {
	var output bytes.Buffer
	if err := printMemberTaskHelp(&output, true, "submit"); err != nil {
		t.Fatal(err)
	}
	var catalog taskHelpCatalog
	if err := json.Unmarshal(output.Bytes(), &catalog); err != nil || len(catalog.Commands) != 1 || catalog.Commands[0].Name != "submit" {
		t.Fatalf("catalog = %+v, err = %v", catalog, err)
	}
	output.Reset()
	if err := printMemberTaskHelp(&output, true, "unknown"); exitCode(err) != 2 {
		t.Fatalf("unknown help exit = %d, err = %v", exitCode(err), err)
	}
}

func TestMemberTaskDefaultOutputIsText(t *testing.T) {
	api := &fakeMemberTaskAPI{response: []membertask.Task{}}
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommandWithFormat(context.Background(), []string{"list"}, api, &stdout, &stderr, false); err != nil {
		t.Fatal(err)
	}
	if json.Valid(stdout.Bytes()) || !bytes.Contains(stdout.Bytes(), []byte("task list")) || !bytes.Contains(stdout.Bytes(), []byte("observed")) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
