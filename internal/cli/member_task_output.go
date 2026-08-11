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
	command := strings.Join(data.Command[1:], " ")
	_, err := fmt.Fprintf(out, "%s\noutcome: %s\nperformed: %t\n", command, data.Outcome, data.Performed)
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
