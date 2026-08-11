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
		return printMemberTaskHelp(os.Stdout, taskJSONRequested())
	}
	client := newMemberTaskClient(a)
	return runMemberTaskCommand(context.Background(), args, client, os.Stdout, os.Stderr)
}

func printMemberTaskHelp(out io.Writer, asJSON bool) error {
	catalog := memberTaskHelpCatalog()
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
	request, err := parseMemberTaskRequest(args)
	if err != nil {
		return reportTaskCommandError(stdout, stderr, commandName(args), taskKeyFromArgs(args), 2, err)
	}
	timeout := time.Duration(taskCommandTimeout(request.Command))*time.Second + 2*time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := api.Execute(ctx, request)
	if err != nil {
		return reportTaskCommandError(stdout, stderr, "task "+request.Command, optionalString(request.TaskKey), classifyTaskError(err), err)
	}
	outcome := outcomeForRequest(request, result)
	stateAfter := stateAfterForResult(result)
	envelope := taskCommandEnvelope{
		SchemaVersion: membertask.SchemaVersion, OK: true, RequestID: newRequestID(), Command: "task " + request.Command,
		TaskKey: optionalString(request.TaskKey), Performed: outcome != membertask.OutcomeObserved && outcome != membertask.OutcomePreviewed,
		Outcome: outcome, StateAfter: stateAfter, ObservedAt: time.Now().UTC(), Data: result, NextCommands: nextCommandsFor(request, result),
	}
	if err := writeTaskEnvelope(stdout, envelope); err != nil {
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

func reportTaskCommandError(stdout, stderr io.Writer, command string, taskKey *string, code int, err error) error {
	taskErr, ok := err.(*membertask.TaskError)
	if !ok {
		taskErr = &membertask.TaskError{Code: "internal_error", Message: "task command failed"}
	}
	envelope := taskCommandEnvelope{SchemaVersion: membertask.SchemaVersion, OK: false, RequestID: newRequestID(), Command: command, TaskKey: taskKey, Outcome: membertask.OutcomeNotCompleted, ObservedAt: time.Now().UTC(), Error: taskErr, NextCommands: []nextCommand{}}
	_ = writeTaskEnvelope(stdout, envelope)
	fmt.Fprintf(stderr, "error[%s]: %s\n", taskErr.Code, taskErr.Message)
	fmt.Fprintln(stderr, "cause: command was not completed")
	fmt.Fprintln(stderr, "next: none")
	fmt.Fprintln(stderr, "manual: inspect the task state with cs-cloud task get when safe")
	return &commandExitError{code: code}
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
	if operation, ok := result.(membertask.Operation); ok && operation.Status == membertask.OperationCompleted {
		if operation.Outcome != "" {
			return operation.Outcome
		}
		return membertask.OutcomeCompleted
	}
	return membertask.OutcomeCompleted
}

func stateAfterForResult(result any) *membertask.Projection {
	switch value := result.(type) {
	case membertask.TaskRecord:
		projection := membertask.ProjectTaskRecord(value)
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
