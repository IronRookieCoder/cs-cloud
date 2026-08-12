package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"cs-cloud/internal/membertask"
)

type nextCommand struct {
	Argv   []string `json:"argv"`
	Safety string   `json:"safety"`
}

type taskCommandData struct {
	RequestID    string                 `json:"request_id"`
	Command      []string               `json:"command"`
	TaskKey      *string                `json:"task_key"`
	Performed    bool                   `json:"performed"`
	Outcome      membertask.Outcome     `json:"outcome"`
	StateBefore  *membertask.Projection `json:"state_before"`
	StateAfter   *membertask.Projection `json:"state_after"`
	ObservedAt   time.Time              `json:"observed_at"`
	Result       any                    `json:"result"`
	NextCommands []nextCommand          `json:"next_commands"`
}

type taskCommandEnvelope struct {
	SchemaVersion string                `json:"schema_version"`
	OK            bool                  `json:"ok"`
	Data          *taskCommandData      `json:"data"`
	Error         *membertask.TaskError `json:"error"`

	RequestID    string                 `json:"-"`
	Command      string                 `json:"-"`
	TaskKey      *string                `json:"-"`
	Performed    bool                   `json:"-"`
	Outcome      membertask.Outcome     `json:"-"`
	StateBefore  *membertask.Projection `json:"-"`
	StateAfter   *membertask.Projection `json:"-"`
	ObservedAt   time.Time              `json:"-"`
	Result       any                    `json:"-"`
	NextCommands []nextCommand          `json:"-"`
}

var errTaskCommandReported = errors.New("task command error already reported")

type commandExitError struct {
	code int
}

func (e *commandExitError) Error() string         { return "task command failed" }
func (e *commandExitError) Unwrap() error         { return errTaskCommandReported }
func (e *commandExitError) ExitCode() int         { return e.code }
func (e *commandExitError) AlreadyReported() bool { return true }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

func CommandExitCode(err error) int { return exitCode(err) }

func CommandErrorReported(err error) bool {
	var reported interface{ AlreadyReported() bool }
	return errors.As(err, &reported) && reported.AlreadyReported()
}

func writeTaskEnvelope(out io.Writer, envelope taskCommandEnvelope) error {
	if envelope.Data == nil {
		envelope.Data = &taskCommandData{
			RequestID: envelope.RequestID, TaskKey: envelope.TaskKey, Performed: envelope.Performed,
			Outcome: envelope.Outcome, StateBefore: envelope.StateBefore, StateAfter: envelope.StateAfter,
			ObservedAt: envelope.ObservedAt, Result: envelope.Result, NextCommands: envelope.NextCommands,
		}
		if envelope.Command != "" {
			envelope.Data.Command = append([]string{"cs-cloud"}, strings.Fields(envelope.Command)...)
		}
	}
	if envelope.Data.NextCommands == nil {
		envelope.Data.NextCommands = []nextCommand{}
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

func writeTaskText(out io.Writer, data *taskCommandData) error {
	if data == nil {
		return errors.New("task command data is missing")
	}
	if len(data.Command) < 2 {
		return errors.New("task command is missing")
	}
	command := strings.Join(data.Command[1:], " ")
	if _, err := fmt.Fprintf(out, "%s\noutcome: %s\nperformed: %t\n", command, data.Outcome, data.Performed); err != nil {
		return err
	}
	if data.StateAfter != nil {
		if _, err := fmt.Fprintf(out, "status: %s\n", displayStatusText(data.StateAfter.DisplayStatus)); err != nil {
			return err
		}
		if len(data.StateAfter.AvailableActions) > 0 {
			if _, err := fmt.Fprintf(out, "next: %s\n", strings.Join(data.StateAfter.AvailableActions, ", ")); err != nil {
				return err
			}
		}
	}
	var displayName, directory string
	switch task := data.Result.(type) {
	case membertask.Task:
		displayName = task.DisplayName
		if task.Local != nil {
			directory = task.Local.Directory
		}
	case membertask.LocalTransition:
		displayName, directory = task.DisplayName, task.Directory
	}
	if task, ok := data.Result.(membertask.Task); ok {
		if task.Remote != nil {
			if task.Remote.IssueIdentifier != "" {
				if _, err := fmt.Fprintf(out, "issue: %s", task.Remote.IssueIdentifier); err != nil {
					return err
				}
				if task.Remote.IssueTitle != "" {
					if _, err := fmt.Fprintf(out, " %s", task.Remote.IssueTitle); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
			}
		} else if task.Local != nil && task.Local.IssueIdentifier != "" {
			if _, err := fmt.Fprintf(out, "issue: %s %s\n", task.Local.IssueIdentifier, task.Local.IssueTitle); err != nil {
				return err
			}
		}
	}
	if displayName != "" {
		if _, err := fmt.Fprintf(out, "task: %s\n", displayName); err != nil {
			return err
		}
	}
	if directory != "" {
		if _, err := fmt.Fprintf(out, "directory: %s\n", directory); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "action: inspect the task details or follow the next command")
	return err
}

func displayStatusText(status membertask.DisplayStatus) string {
	switch status {
	case membertask.StatusEnded:
		return "已结束"
	case membertask.StatusSubmitting:
		return "提交中"
	case membertask.StatusInProgress:
		return "处理中"
	case membertask.StatusPrepared:
		return "已准备"
	case membertask.StatusNotPrepared:
		return "待准备"
	case membertask.StatusReadOnly:
		return "只可查看"
	case membertask.StatusReprepareRequired:
		return "需要重新准备"
	case membertask.StatusReconfirmationRequired:
		return "需要重新确认"
	case membertask.StatusSyncPending:
		return "等待同步"
	case membertask.StatusConfirmationWaiting:
		return "等待确认"
	default:
		return string(status)
	}
}

func writeTaskErrorText(out io.Writer, envelope taskCommandEnvelope) error {
	if envelope.Error == nil || envelope.Data == nil {
		return errors.New("task error envelope is incomplete")
	}
	if _, err := fmt.Fprintf(out, "error[%s]: %s\ncause: command was not completed\n", envelope.Error.Code, envelope.Error.Message); err != nil {
		return err
	}
	if len(envelope.Data.NextCommands) == 0 {
		if _, err := fmt.Fprintln(out, "next: none"); err != nil {
			return err
		}
	} else {
		for _, command := range envelope.Data.NextCommands {
			if _, err := fmt.Fprintf(out, "next: %s [%s]\n", strings.Join(command.Argv, " "), command.Safety); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(out, "manual: inspect the task state with cs-cloud task get when safe")
	return err
}

func (e *taskCommandEnvelope) UnmarshalJSON(payload []byte) error {
	type wireEnvelope struct {
		SchemaVersion string                `json:"schema_version"`
		OK            bool                  `json:"ok"`
		Data          *taskCommandData      `json:"data"`
		Error         *membertask.TaskError `json:"error"`
	}
	var wire wireEnvelope
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	e.SchemaVersion, e.OK, e.Data, e.Error = wire.SchemaVersion, wire.OK, wire.Data, wire.Error
	if wire.Data != nil {
		e.RequestID, e.TaskKey, e.Performed = wire.Data.RequestID, wire.Data.TaskKey, wire.Data.Performed
		e.Outcome, e.StateBefore, e.StateAfter = wire.Data.Outcome, wire.Data.StateBefore, wire.Data.StateAfter
		e.ObservedAt, e.Result, e.NextCommands = wire.Data.ObservedAt, wire.Data.Result, wire.Data.NextCommands
		if len(wire.Data.Command) > 1 {
			e.Command = strings.Join(wire.Data.Command[1:], " ")
		}
	}
	return nil
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("time-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
