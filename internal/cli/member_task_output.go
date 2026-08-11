package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"cs-cloud/internal/membertask"
)

type nextCommand struct {
	Argv   []string `json:"argv"`
	Safety string   `json:"safety"`
}

type taskCommandEnvelope struct {
	SchemaVersion string                 `json:"schema_version"`
	OK            bool                   `json:"ok"`
	RequestID     string                 `json:"request_id"`
	Command       string                 `json:"command"`
	TaskKey       *string                `json:"task_key"`
	Performed     bool                   `json:"performed"`
	Outcome       membertask.Outcome     `json:"outcome"`
	StateBefore   *membertask.Projection `json:"state_before"`
	StateAfter    *membertask.Projection `json:"state_after"`
	ObservedAt    time.Time              `json:"observed_at"`
	Data          any                    `json:"data"`
	Error         *membertask.TaskError  `json:"error"`
	NextCommands  []nextCommand          `json:"next_commands"`
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
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("time-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
