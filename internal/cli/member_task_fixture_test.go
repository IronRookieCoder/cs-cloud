package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cs-cloud/internal/membertask"
)

func TestTaskFixtureFlagIsDeclaredForExecutableCommands(t *testing.T) {
	for _, command := range decodeTaskHelpCatalog(t).Commands {
		if command.Name == "help" {
			continue
		}
		found := false
		for _, argument := range command.Arguments {
			if argument.Name == "fixture" {
				found = argument.Kind == "flag" && argument.Flag == "--fixture" && argument.Type == "string" && argument.ValueStyle == "equals_or_unprefixed_separate"
			}
		}
		if !found {
			t.Fatalf("command %q does not declare the fixture flag: %+v", command.Name, command.Arguments)
		}
	}
}

func TestTaskFixtureReviewPreviewBindsFixtureToConfirmation(t *testing.T) {
	fixturePath := writeMemberTaskFixture(t)
	api := &fixtureMemberTaskAPI{}
	key := "zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic"
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"review", key, "--decision=approve", "--fixture=" + fixturePath}, api, &stdout, &stderr); err != nil {
		t.Fatalf("review preview: %v, stderr=%s", err, stderr.String())
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview: %v: %s", err, stdout.String())
	}
	want := []string{"cs-cloud", "task", "review", key, "--confirm", "review-preview-1", "--fixture=" + fixturePath}
	if envelope.Data == nil || envelope.Outcome != membertask.OutcomePreviewed || len(envelope.Data.NextCommands) != 1 || !reflect.DeepEqual(envelope.Data.NextCommands[0].Argv, want) {
		t.Fatalf("preview envelope = %s", stdout.String())
	}
}

func TestTaskFixtureGetReturnsCapturedIssueAndReviewArtifact(t *testing.T) {
	fixturePath := writeMemberTaskFixture(t)
	api := &fixtureMemberTaskAPI{}
	key := "zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic"
	result, err := api.Execute(context.Background(), memberTaskRequest{Command: "get", TaskKey: key, FixturePath: fixturePath})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := result.(membertask.Task)
	if !ok || task.Context == nil {
		t.Fatalf("task = %#v", result)
	}
	if task.Context.Goal != "审查 COS-155 用原生web技术创建一个五子棋游戏-2100 的方案设计成果" {
		t.Fatalf("goal = %q", task.Context.Goal)
	}
	if len(task.Context.RequiredDeliverables) != 1 || task.Context.RequiredDeliverables[0].Title != "五子棋游戏方案设计文档" || task.Context.RequiredDeliverables[0].URL != "https://git-common-zgsm.sangfor.com/t-a74c21e4/wf-b7d47227/pulls/3" {
		t.Fatalf("deliverables = %+v", task.Context.RequiredDeliverables)
	}
	if len(task.Context.Repositories) != 1 || task.Context.Repositories[0].HeadSHA != "b015f800da59abd2cfdb3795de8201b449bcc982" {
		t.Fatalf("repositories = %+v", task.Context.Repositories)
	}
}

func TestTaskFixtureReviewConfirmationCompletes(t *testing.T) {
	fixturePath := writeMemberTaskFixture(t)
	api := &fixtureMemberTaskAPI{}
	key := "zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic"
	var stdout, stderr bytes.Buffer
	if err := runMemberTaskCommand(context.Background(), []string{"review", key, "--confirm=review-preview-1", "--fixture=" + fixturePath}, api, &stdout, &stderr); err != nil {
		t.Fatalf("review confirm: %v, stderr=%s", err, stderr.String())
	}
	var envelope taskCommandEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode confirmation: %v: %s", err, stdout.String())
	}
	operation, ok := envelope.Result.(map[string]any)
	if !envelope.OK || envelope.Outcome != membertask.OutcomeCompleted || !envelope.Performed || !ok || operation["id"] != "review-operation-1" {
		t.Fatalf("confirmation envelope = %s", stdout.String())
	}
}

func TestTaskFixtureReviewConfirmationUsesTheBoundDecision(t *testing.T) {
	fixturePath := writeMemberTaskFixture(t)
	api := &fixtureMemberTaskAPI{}
	key := "zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic"
	result, err := api.Execute(context.Background(), memberTaskRequest{Command: "review", TaskKey: key, PreviewID: "review-preview-reject", FixturePath: fixturePath})
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := result.(membertask.Operation)
	if !ok || operation.Kind != "reject" || operation.PreviewID != "review-preview-reject" {
		t.Fatalf("operation = %#v", result)
	}
}

func writeMemberTaskFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-review.json")
	data := `{
  "schema_version": "1.0",
  "task_key": "zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic",
  "responses": {
	"get": {
	  "task_key": {
		"cloud_instance_id": "zgsm",
		"workspace_id": "costrict",
		"node_run_id": "006485da-3f92-443e-8c5d-677bfbd82225",
		"role": "critic"
	  },
	  "remote": {
		"cloud_instance_id": "zgsm",
		"workspace_id": "costrict",
		"node_run_id": "006485da-3f92-443e-8c5d-677bfbd82225",
		"role": "critic",
		"attempt": 1,
		"task_version": 1,
		"context_version": 1,
		"cloud_status": "assigned"
	  },
	  "context": {
		"cloud_instance_id": "zgsm",
		"workspace_id": "costrict",
		"node_run_id": "006485da-3f92-443e-8c5d-677bfbd82225",
		"role": "critic",
		"attempt": 1,
		"task_version": 1,
		"context_version": 1,
		"cloud_status": "assigned",
		"goal": "审查 COS-155 用原生web技术创建一个五子棋游戏-2100 的方案设计成果",
		"objective": "检查方案设计师提交的五子棋游戏方案设计文档，并决定通过或驳回。",
		"acceptance_criteria": [
		  "使用原生 HTML5、CSS3 和 JavaScript，零第三方依赖",
		  "标准 15x15 棋盘，双人同屏轮流落子，黑方先手",
		  "覆盖横、竖、斜线五连胜、满盘平局、重复落子和重新开始"
		],
		"required_deliverables": [{
		  "id": "gomoku-solution-design",
		  "title": "五子棋游戏方案设计文档",
		  "description": "gomoku-solution-design.md，共 133 行新增",
		  "required": true,
		  "purpose": "方案设计审查",
		  "missing": false,
		  "url": "https://git-common-zgsm.sangfor.com/t-a74c21e4/wf-b7d47227/pulls/3"
		}],
		"prepare_allowed": true,
		"providers": ["gitea"],
		"repositories": [{
		  "provider": "gitea",
		  "identity": "t-a74c21e4/wf-b7d47227",
		  "clone_url": "https://git-common-zgsm.sangfor.com/t-a74c21e4/wf-b7d47227.git",
		  "base_ref": "inst-97cf6dd2",
		  "target_ref": "node/01-0447d3a1",
		  "head_sha": "b015f800da59abd2cfdb3795de8201b449bcc982",
		  "prepare_allowed": true
		}],
		"material_digest": "sha256:costrict-006485da-solution-review"
	  },
	  "projection": {
		"display_status": "not_prepared",
		"flags": {},
		"available_actions": ["get", "prepare"]
	  }
	},
    "review_approve_preview": {
      "id": "review-preview-1",
      "kind": "approve",
      "status": "previewed",
      "task_version": 1,
      "context_version": 1,
      "attempt": 1,
      "material_digest": "sha256:material",
      "content_digest": "sha256:content",
      "preview_digest": "sha256:preview",
      "review_snapshot_id": "review-snapshot-1",
      "expires_at": "2099-01-01T00:00:00Z",
      "publish_plan": {"decision":"approve"}
    },
	"review_reject_preview": {
	  "id": "review-preview-reject",
	  "kind": "reject",
	  "status": "previewed",
	  "task_version": 1,
	  "context_version": 1,
	  "attempt": 1,
	  "material_digest": "sha256:material",
	  "content_digest": "sha256:content",
	  "preview_digest": "sha256:preview-reject",
	  "review_snapshot_id": "review-snapshot-1",
	  "expires_at": "2099-01-01T00:00:00Z",
	  "publish_plan": {"decision":"reject"}
	},
    "review_confirm:review-preview-1": {
      "id": "review-operation-1",
      "preview_id": "review-preview-1",
      "kind": "approve",
      "status": "completed",
      "outcome": "completed"
	},
	"review_confirm:review-preview-reject": {
	  "id": "review-operation-reject",
	  "preview_id": "review-preview-reject",
	  "kind": "reject",
	  "status": "completed",
	  "outcome": "completed"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
