package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"cs-cloud/internal/membertask"
)

const maxMemberTaskFixtureBytes = 8 << 20

type fixtureMemberTaskAPI struct {
	fallback memberTaskAPI
}

type memberTaskFixture struct {
	SchemaVersion string                     `json:"schema_version"`
	TaskKey       string                     `json:"task_key"`
	Responses     map[string]json.RawMessage `json:"responses"`
}

func (api *fixtureMemberTaskAPI) Execute(ctx context.Context, request memberTaskRequest) (any, error) {
	if request.FixturePath == "" {
		if api.fallback == nil {
			return nil, fixtureTaskError("fixture path is required")
		}
		return api.fallback.Execute(ctx, request)
	}
	fixture, err := loadMemberTaskFixture(request.FixturePath)
	if err != nil {
		return nil, err
	}
	if request.Command != "list" && request.TaskKey != fixture.TaskKey {
		return nil, fixtureTaskError("fixture task key does not match the requested task")
	}
	responseName := fixtureResponseName(request)
	if request.PreviewID != "" {
		responseName += ":" + request.PreviewID
	}
	raw, ok := fixture.Responses[responseName]
	if !ok {
		return nil, fixtureTaskError("fixture does not define response " + responseName)
	}
	return decodeMemberTaskFixtureResponse(request, raw)
}

func loadMemberTaskFixture(path string) (memberTaskFixture, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxMemberTaskFixtureBytes {
		return memberTaskFixture{}, fixtureTaskError("fixture file is unavailable")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return memberTaskFixture{}, fixtureTaskError("fixture file is unavailable")
	}
	var fixture memberTaskFixture
	if err := json.Unmarshal(payload, &fixture); err != nil || fixture.SchemaVersion != membertask.SchemaVersion || fixture.Responses == nil {
		return memberTaskFixture{}, fixtureTaskError("fixture file is invalid or incompatible")
	}
	if _, err := membertask.ParseTaskKey(fixture.TaskKey); err != nil {
		return memberTaskFixture{}, fixtureTaskError("fixture task key is invalid")
	}
	return fixture, nil
}

func fixtureResponseName(request memberTaskRequest) string {
	if request.Command == "review" && request.PreviewID == "" {
		return "review_" + request.Decision + "_preview"
	}
	if request.Command == "submit" && request.PreviewID == "" {
		return "submit_preview"
	}
	if (request.Command == "review" || request.Command == "submit") && request.PreviewID != "" {
		return request.Command + "_confirm"
	}
	if request.Command == "delete" {
		if request.PreviewID != "" {
			return "delete_confirm"
		}
		return "delete_preview"
	}
	return request.Command
}

func decodeMemberTaskFixtureResponse(request memberTaskRequest, raw json.RawMessage) (any, error) {
	var target any
	switch request.Command {
	case "list":
		target = &[]membertask.Task{}
	case "get":
		target = &membertask.Task{}
	case "prepare", "start", "pause":
		target = &membertask.LocalTransition{}
	case "submit", "review":
		if request.PreviewID == "" {
			target = &membertask.Preview{}
		} else {
			target = &membertask.Operation{}
		}
	case "delete":
		if request.PreviewID == "" {
			target = &membertask.DeletePreview{}
		} else {
			var deleted map[string]bool
			target = &deleted
		}
	default:
		return nil, fixtureTaskError("fixture command is unsupported")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fixtureTaskError("fixture response is invalid")
	}
	if operation, ok := target.(*membertask.Operation); ok && operation.PreviewID != request.PreviewID {
		return nil, fixtureTaskError("fixture confirmation does not match the preview")
	}
	switch value := target.(type) {
	case *[]membertask.Task:
		return *value, nil
	case *membertask.Task:
		return *value, nil
	case *membertask.LocalTransition:
		return *value, nil
	case *membertask.Preview:
		return *value, nil
	case *membertask.Operation:
		return *value, nil
	case *membertask.DeletePreview:
		return *value, nil
	case *map[string]bool:
		return *value, nil
	default:
		return nil, errors.New("unreachable fixture response type")
	}
}

func fixtureTaskError(message string) error {
	return &membertask.TaskError{Code: "invalid_fixture", Message: message}
}
