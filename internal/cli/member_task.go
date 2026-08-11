package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/membertask"
)

type memberTaskRequest struct {
	Command    string
	TaskKey    string
	PreviewID  string
	Decision   string
	Reason     string
	DeleteMode membertask.DeleteMode
}

type memberTaskAPI interface {
	Execute(context.Context, memberTaskRequest) (any, error)
}

func memberTaskCmd(a *app.App, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		var command []string
		if len(args) > 1 {
			command = args[1:]
		}
		return printMemberTaskHelp(os.Stdout, taskJSONRequested(), command...)
	}
	client := newMemberTaskClient(a)
	return runMemberTaskCommandWithFormat(context.Background(), args, client, os.Stdout, os.Stderr, taskJSONRequested())
}

func printMemberTaskHelp(out io.Writer, asJSON bool, command ...string) error {
	catalog := memberTaskHelpCatalog()
	if len(command) > 1 {
		return &commandExitError{code: 2}
	}
	if len(command) == 1 {
		found := false
		for _, spec := range catalog.Commands {
			if spec.Name == command[0] {
				catalog.Commands = []CommandSpec{spec}
				found = true
				break
			}
		}
		if !found {
			return &commandExitError{code: 2}
		}
	}
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(catalog)
	}
	fmt.Fprintln(out, "cs-cloud task")
	fmt.Fprintln(out, "Usage: cs-cloud task <command> [arguments]")
	for _, command := range catalog.Commands {
		fmt.Fprintf(out, "  %-8s %s\n", command.Name, command.Summary)
	}
	return nil
}

func runMemberTaskCommand(parent context.Context, args []string, api memberTaskAPI, stdout, stderr io.Writer) error {
	return runMemberTaskCommandWithFormat(parent, args, api, stdout, stderr, true)
}

func runMemberTaskCommandWithFormat(parent context.Context, args []string, api memberTaskAPI, stdout, stderr io.Writer, asJSON bool) error {
	request, err := parseMemberTaskRequest(args)
	if err != nil {
		return reportTaskCommandError(stdout, stderr, args, taskKeyFromArgs(args), 2, err, asJSON)
	}
	timeout := time.Duration(taskCommandTimeout(request.Command))*time.Second + 2*time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := api.Execute(ctx, request)
	if err != nil {
		return reportTaskCommandError(stdout, stderr, args, optionalString(request.TaskKey), classifyTaskError(err), err, asJSON)
	}
	outcome := outcomeForRequest(request, result)
	performed := performedForResult(result, outcome)
	stateAfter := stateAfterForResult(result)
	envelope := taskCommandEnvelope{
		SchemaVersion: membertask.SchemaVersion, OK: true,
		Data: &taskCommandData{
			RequestID: newRequestID(), Command: taskCommandArgv(args), TaskKey: optionalString(request.TaskKey),
			Performed: performed, Outcome: outcome, StateAfter: stateAfter, ObservedAt: time.Now().UTC(),
			Result: result, NextCommands: nextCommandsFor(request, result),
		},
	}
	if asJSON {
		if err := writeTaskEnvelope(stdout, envelope); err != nil {
			return err
		}
	} else if err := writeTaskText(stdout, envelope.Data); err != nil {
		return err
	}
	return nil
}

func parseMemberTaskRequest(args []string) (memberTaskRequest, error) {
	if len(args) == 0 {
		return memberTaskRequest{}, &membertask.TaskError{Code: "invalid_arguments", Message: "task command is required"}
	}
	command := args[0]
	request := memberTaskRequest{Command: command}
	switch command {
	case "list":
		if len(args) != 1 {
			return request, invalidArguments("list accepts no arguments")
		}
		return request, nil
	case "get", "prepare", "start", "pause":
		if len(args) != 2 {
			return request, invalidArguments(command + " requires exactly one task key")
		}
		request.TaskKey = args[1]
		return validateRequestTaskKey(request)
	case "submit", "review", "delete":
		if len(args) < 2 {
			return request, invalidArguments(command + " requires a task key")
		}
		request.TaskKey = args[1]
		if _, err := membertask.ParseTaskKey(request.TaskKey); err != nil {
			return request, invalidArguments("task key is invalid")
		}
	default:
		return request, invalidArguments("unknown task command: " + command)
	}
	flags, err := parseMemberTaskFlags(args[2:])
	if err != nil {
		return request, err
	}
	request.PreviewID = flags["confirm"]
	switch command {
	case "submit":
		if len(flags) > boolMapLen(flags, "confirm") {
			return request, invalidArguments("submit accepts only --confirm")
		}
	case "review":
		request.Decision = flags["decision"]
		request.Reason = flags["reason"]
		if request.PreviewID != "" {
			if len(flags) != 1 {
				return request, invalidArguments("--confirm cannot be combined with review preview arguments")
			}
		} else {
			if request.Decision != "approve" && request.Decision != "reject" {
				return request, invalidArguments("review requires --decision approve or reject")
			}
			if request.Decision == "reject" && strings.TrimSpace(request.Reason) == "" {
				return request, invalidArguments("reject requires --reason")
			}
			for name := range flags {
				if name != "decision" && name != "reason" {
					return request, invalidArguments("review flag is not supported: --" + name)
				}
			}
		}
	case "delete":
		request.DeleteMode = membertask.DeleteModeNormal
		if flags["force-discard"] == "true" {
			request.DeleteMode = membertask.DeleteModeForceDiscard
		}
		if request.PreviewID != "" && len(flags) != 1 {
			return request, invalidArguments("--confirm cannot be combined with delete preview arguments")
		}
		for name := range flags {
			if name != "confirm" && name != "force-discard" {
				return request, invalidArguments("delete flag is not supported: --" + name)
			}
		}
	}
	return request, nil
}

func validateRequestTaskKey(request memberTaskRequest) (memberTaskRequest, error) {
	if _, err := membertask.ParseTaskKey(request.TaskKey); err != nil {
		return request, invalidArguments("task key is invalid")
	}
	return request, nil
}

func parseMemberTaskFlags(args []string) (map[string]string, error) {
	flags := make(map[string]string)
	valueFlags := map[string]bool{"confirm": true, "decision": true, "reason": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return nil, invalidArguments("unexpected positional argument: " + arg)
		}
		name := strings.TrimPrefix(arg, "--")
		value := "true"
		if before, after, ok := strings.Cut(name, "="); ok {
			name, value = before, after
		} else if valueFlags[name] {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, invalidArguments("--" + name + " requires a value")
			}
			i++
			value = args[i]
		}
		if _, exists := flags[name]; exists || name == "" || value == "" {
			return nil, invalidArguments("invalid or duplicate flag: --" + name)
		}
		flags[name] = value
	}
	return flags, nil
}

func invalidArguments(message string) error {
	return &membertask.TaskError{Code: "invalid_arguments", Message: message}
}

func reportTaskCommandError(stdout, stderr io.Writer, args []string, taskKey *string, code int, err error, asJSON bool) error {
	taskErr, ok := err.(*membertask.TaskError)
	if !ok {
		taskErr = &membertask.TaskError{Code: "internal_error", Message: "task command failed"}
	}
	envelope := taskCommandEnvelope{SchemaVersion: membertask.SchemaVersion, OK: false, Error: taskErr, Data: &taskCommandData{
		RequestID: newRequestID(), Command: taskCommandArgv(args), TaskKey: taskKey, Outcome: membertask.OutcomeNotCompleted,
		ObservedAt: time.Now().UTC(), NextCommands: failureNextCommands(taskErr.Code, args, taskKey),
	}}
	if asJSON {
		_ = writeTaskEnvelope(stdout, envelope)
	}
	fmt.Fprintf(stderr, "error[%s]: %s\n", taskErr.Code, taskErr.Message)
	fmt.Fprintln(stderr, "cause: command was not completed")
	fmt.Fprintln(stderr, "next: none")
	fmt.Fprintln(stderr, "manual: inspect the task state with cs-cloud task get when safe")
	return &commandExitError{code: code}
}

func taskCommandArgv(args []string) []string {
	return append([]string{"cs-cloud", "task"}, args...)
}

func failureNextCommands(code string, args []string, taskKey *string) []nextCommand {
	if taskKey == nil || len(args) == 0 {
		return []nextCommand{}
	}
	switch code {
	case "preview_stale", "review_snapshot_stale":
		if args[0] == "submit" || args[0] == "delete" {
			return []nextCommand{{Argv: []string{"cs-cloud", "task", args[0], *taskKey}, Safety: "preview_only"}}
		}
	case "operation_result_unknown":
		return []nextCommand{{Argv: []string{"cs-cloud", "task", "get", *taskKey}, Safety: "observe_only"}}
	case "force_discard_required":
		return []nextCommand{{Argv: []string{"cs-cloud", "task", "delete", *taskKey, "--force-discard"}, Safety: "preview_only"}}
	}
	return []nextCommand{}
}

func classifyTaskError(err error) int {
	taskErr, ok := err.(*membertask.TaskError)
	if !ok {
		return 1
	}
	switch taskErr.Code {
	case "cloud_unavailable", "local_transport_unavailable", "daemon_unavailable", "operation_result_unknown":
		return 3
	case "invalid_arguments", "invalid_task_key":
		return 2
	case "local_task_store_corrupt", "invalid_cloud_response", "internal_error":
		return 1
	case "material_content_unavailable", "repository_access_unavailable":
		return 4
	default:
		return 4
	}
}

func outcomeForRequest(request memberTaskRequest, result any) membertask.Outcome {
	if request.Command == "list" || request.Command == "get" {
		return membertask.OutcomeObserved
	}
	if (request.Command == "submit" || request.Command == "review" || request.Command == "delete") && request.PreviewID == "" {
		return membertask.OutcomePreviewed
	}
	if transition, ok := result.(membertask.LocalTransition); ok {
		return transition.Outcome
	}
	if operation, ok := result.(membertask.Operation); ok {
		if operation.Status != membertask.OperationCompleted {
			return membertask.OutcomeNotCompleted
		}
		if operation.Outcome != "" {
			return operation.Outcome
		}
		return membertask.OutcomeCompleted
	}
	return membertask.OutcomeCompleted
}

func performedForResult(result any, outcome membertask.Outcome) bool {
	if transition, ok := result.(membertask.LocalTransition); ok {
		return transition.Performed
	}
	return outcome != membertask.OutcomeObserved && outcome != membertask.OutcomePreviewed && outcome != membertask.OutcomeNotCompleted && outcome != membertask.OutcomeAlreadyCompleted
}

func stateAfterForResult(result any) *membertask.Projection {
	switch value := result.(type) {
	case membertask.TaskRecord:
		projection := membertask.ProjectTaskRecord(value)
		return &projection
	case membertask.LocalTransition:
		projection := membertask.ProjectTaskRecord(value.TaskRecord)
		return &projection
	case membertask.Task:
		projection := value.Projection
		return &projection
	default:
		return nil
	}
}

func nextCommandsFor(request memberTaskRequest, result any) []nextCommand {
	preview, ok := result.(membertask.Preview)
	if !ok {
		if deletion, ok := result.(membertask.DeletePreview); ok {
			return []nextCommand{{Argv: []string{"cs-cloud", "task", "delete", request.TaskKey, "--confirm", deletion.ID}, Safety: "requires_confirmation"}}
		}
		return []nextCommand{}
	}
	return []nextCommand{{Argv: []string{"cs-cloud", "task", request.Command, request.TaskKey, "--confirm", preview.ID}, Safety: "requires_confirmation"}}
}

func taskJSONRequested() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--json" || arg == "--json=true" || arg == "--json=1" {
			return true
		}
	}
	return false
}

func commandName(args []string) string {
	if len(args) == 0 {
		return "task"
	}
	return "task " + args[0]
}

func taskKeyFromArgs(args []string) *string {
	if len(args) < 2 {
		return nil
	}
	if _, err := membertask.ParseTaskKey(args[1]); err != nil {
		return nil
	}
	return optionalString(args[1])
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolMapLen(flags map[string]string, name string) int {
	if _, ok := flags[name]; ok {
		return 1
	}
	return 0
}
