package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cs-cloud/internal/membertask"
)

const maxMemberTaskFixtureBytes = 8 << 20

type fixtureMemberTaskAPI struct {
	fallback memberTaskAPI
}

type memberTaskFixture struct {
	SchemaVersion     string                     `json:"schema_version"`
	TaskKey           string                     `json:"task_key"`
	MaterialDirectory string                     `json:"material_directory,omitempty"`
	MaterialFiles     []string                   `json:"material_files,omitempty"`
	Responses         map[string]json.RawMessage `json:"responses"`
	resolvedMaterials string
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
	result, err := decodeMemberTaskFixtureResponse(request, raw)
	if err != nil {
		return nil, err
	}
	if transition, ok := result.(membertask.LocalTransition); ok && fixture.resolvedMaterials != "" {
		transition.Directory = fixture.resolvedMaterials
		return transition, nil
	}
	return result, nil
}

func loadMemberTaskFixture(path string) (memberTaskFixture, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return memberTaskFixture{}, fixtureTaskError("fixture file is unavailable")
	}
	info, err := os.Stat(absolutePath)
	if err != nil || info.IsDir() || info.Size() > maxMemberTaskFixtureBytes {
		return memberTaskFixture{}, fixtureTaskError("fixture file is unavailable")
	}
	payload, err := os.ReadFile(absolutePath)
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
	if fixture.MaterialDirectory != "" {
		root, err := resolveFixtureMaterialPath(filepath.Dir(absolutePath), fixture.MaterialDirectory, true)
		if err != nil {
			return memberTaskFixture{}, err
		}
		for _, material := range fixture.MaterialFiles {
			if _, err := resolveFixtureMaterialPath(root, material, false); err != nil {
				return memberTaskFixture{}, err
			}
		}
		fixture.resolvedMaterials = root
	} else if len(fixture.MaterialFiles) != 0 {
		return memberTaskFixture{}, fixtureTaskError("fixture material directory is required")
	}
	return fixture, nil
}

func resolveFixtureMaterialPath(base, relative string, wantDirectory bool) (string, error) {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", fixtureTaskError("fixture material path is invalid")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fixtureTaskError("fixture material path is invalid")
	}
	basePath, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fixtureTaskError("fixture material path is unavailable")
	}
	targetPath, err := filepath.EvalSymlinks(filepath.Join(basePath, clean))
	if err != nil {
		return "", fixtureTaskError("fixture material path is unavailable")
	}
	rel, err := filepath.Rel(basePath, targetPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fixtureTaskError("fixture material path escapes fixture directory")
	}
	info, err := os.Stat(targetPath)
	if err != nil || (wantDirectory && !info.IsDir()) || (!wantDirectory && !info.Mode().IsRegular()) {
		return "", fixtureTaskError("fixture material path is unavailable")
	}
	return targetPath, nil
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
