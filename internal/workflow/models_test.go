package workflow

import (
	"encoding/json"
	"testing"
)

func TestTaskRunPayloadReposAndDeliverables(t *testing.T) {
	raw := `{"task_id":"t1","agent":"csc","prompt":"p",
		"repos":[{"url":"https://gitlab/o/r.git","provider":"gitlab","role":"code"}],
		"deliverables":[{"id":"d1","kind":"pull_request","report":{"endpoint":"/x","method":"POST","body_field":"pull_request_url"}}]}`
	var p TaskRunPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Repos) != 1 || p.Repos[0].URL != "https://gitlab/o/r.git" {
		t.Errorf("repos: %+v", p.Repos)
	}
	if len(p.Deliverables) != 1 || p.Deliverables[0].Kind != "pull_request" {
		t.Errorf("deliverables: %+v", p.Deliverables)
	}
	if p.Deliverables[0].Report.Endpoint != "/x" {
		t.Errorf("report endpoint: %+v", p.Deliverables[0].Report)
	}
}

func TestTaskRunPayloadPriorSession(t *testing.T) {
	raw := `{"task_id":"t1","agent":"csc","prompt":"p",
		"prior_session_id":"sess-x","prior_work_dir":"/prior/dir"}`
	var p TaskRunPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.PriorSessionID != "sess-x" {
		t.Errorf("prior_session_id = %q", p.PriorSessionID)
	}
	if p.PriorWorkDir != "/prior/dir" {
		t.Errorf("prior_work_dir = %q", p.PriorWorkDir)
	}
}
