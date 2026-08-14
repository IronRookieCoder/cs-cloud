package membertask

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateDeliverableBindingsRequiresSafeKnownRegularFiles(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "output", "result.html")
	if err := os.WriteFile(file, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(TaskRecord{Key: key, Directory: dir, Prepared: true, RemoteVersion: "1:1:1"}); err != nil {
		t.Fatal(err)
	}
	srv := taskContextServer(t, `{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"prepare_allowed":true,"required_deliverables":[{"id":"delivery-1","required":true}]}`)
	defer srv.Close()
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	valid := []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/result.html"}}
	if err := svc.validateDeliverableBindings(context.Background(), key, TaskRecord{Key: key, Directory: dir}, valid); err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	for _, test := range []struct {
		name    string
		binding []DeliverableFileBinding
	}{
		{name: "duplicate", binding: []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/result.html"}, {DeliverableID: "delivery-1", File: "output/result.html"}}},
		{name: "unknown", binding: []DeliverableFileBinding{{DeliverableID: "missing", File: "output/result.html"}}},
		{name: "manifest", binding: []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "manifest.json"}}},
		{name: "escape", binding: []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "../outside.html"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := svc.validateDeliverableBindings(context.Background(), key, TaskRecord{Key: key, Directory: dir}, test.binding)
			te, ok := err.(*TaskError)
			if !ok || te.Code != "deliverable_binding_invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(file, filepath.Join(dir, "output", "link.html")); err != nil {
			t.Fatal(err)
		}
		err := svc.validateDeliverableBindings(context.Background(), key, TaskRecord{Key: key, Directory: dir}, []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/link.html"}})
		te, ok := err.(*TaskError)
		if !ok || te.Code != "deliverable_binding_invalid" {
			t.Fatalf("symlink error = %#v", err)
		}
	}
}

func TestBuildSubmitManifestRejectsUncommittedRepositoryOutput(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "init")
	runGitTest(t, repoDir, "config", "user.email", "test@example.com")
	runGitTest(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "result.txt"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "result.txt")
	runGitTest(t, repoDir, "commit", "-m", "base")
	base := runGitTest(t, repoDir, "rev-parse", "HEAD")
	sources := []MaterialSource{{Identity: "repo", Kind: "git", RelativePath: "repo", Role: MaterialOutputWritable, Repository: &RepositoryContext{Identity: "repo", BaseSHA: base, TargetRef: "refs/heads/tasks/result", OutputPaths: []string{"result.txt"}}}}
	manifest, err := BuildManifest(dir, sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "result.txt"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = buildSubmitManifest(TaskRecord{Key: key, Directory: dir, Manifest: &manifest}, verification)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "uncommitted_changes_present" {
		t.Fatalf("error = %#v", err)
	}
}

func TestBuildSubmitManifestBindsFileContentToHash(t *testing.T) {
	store, key, output := seedOperationTask(t)
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, files, err := buildSubmitManifest(record, verification)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(output)
	if len(files) != 1 || files[0].Content != string(content) || digestJSON(files[0].Content) == files[0].SHA256 {
		t.Fatalf("files = %+v", files)
	}
	wantHash, _ := hashFile(output)
	if files[0].SHA256 != wantHash {
		t.Fatalf("sha256 = %q, want %q", files[0].SHA256, wantHash)
	}
}

func TestBuildSubmitManifestUsesOnlyExplicitDeliverableBindings(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output", "z.html"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output", "a.html"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: SnapshotSchemaVersion, Files: []FileManifest{
		{Identity: "z", RelativePath: "output/z.html", Role: MaterialOutputWritable, SHA256: digestJSON("z")},
		{Identity: "a", RelativePath: "output/a.html", Role: MaterialOutputWritable, SHA256: digestJSON("a")},
	}}
	verification := Verification{}
	_, files, err := buildSubmitManifest(TaskRecord{Key: key, Directory: dir, Manifest: &manifest}, verification, []DeliverableFileBinding{{DeliverableID: "z", File: "output/z.html"}, {DeliverableID: "a", File: "output/a.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Identity != "a" || files[1].Identity != "z" || files[0].Name != "a.html" || files[1].Name != "z.html" || files[0].SourceVersion != "sha256:ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" || files[1].SourceVersion != "sha256:594e519ae499312b29433b7dd8a97ff068defcba9755b6d5d00e84c524d67b06" {
		t.Fatalf("files = %#v", files)
	}
	if _, files, err := buildSubmitManifest(TaskRecord{Key: key, Directory: dir, Manifest: &manifest}, verification, []DeliverableFileBinding{}); err != nil || len(files) != 0 {
		t.Fatalf("empty explicit bindings files=%#v err=%v", files, err)
	}
}

func TestBuildSubmitManifestRejectsParentSymlinkSwapOutsideTaskRoot(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	outputDir := filepath.Join(dir, "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "result.html"), []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, Files: []FileManifest{{Identity: "delivery-1", RelativePath: "output/result.html", Role: MaterialOutputWritable}}}
	record := TaskRecord{Key: key, Directory: dir, Manifest: &manifest}
	binding := []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/result.html"}}

	srv := taskContextServer(t, `{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"required_deliverables":[{"id":"delivery-1","required":true}]}`)
	defer srv.Close()
	if err := NewService(store, NewCloudClient(srv.URL, testCredentials)).validateDeliverableBindings(context.Background(), key, record, binding); err != nil {
		t.Fatalf("validateDeliverableBindings: %v", err)
	}

	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalDir, "result.html"), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outputDir, filepath.Join(dir, "validated-output")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDir, outputDir); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, _, err := buildSubmitManifest(record, Verification{}, binding)
	var taskErr *TaskError
	if !errors.As(err, &taskErr) || taskErr.Code != "deliverable_binding_invalid" {
		t.Fatalf("buildSubmitManifest error = %#v", err)
	}
}

func TestBuildSubmitManifestRejectsOversizedExplicitBinding(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "output", "large.bin")
	if err := os.WriteFile(path, make([]byte, maxSubmissionFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: SnapshotSchemaVersion, Files: []FileManifest{{Identity: "delivery-1", RelativePath: "output/large.bin", Role: MaterialOutputWritable}}}
	_, _, err := buildSubmitManifest(TaskRecord{Key: key, Directory: dir, Manifest: &manifest}, Verification{}, []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/large.bin"}})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "deliverable_binding_invalid" {
		t.Fatalf("error = %#v", err)
	}
}

func TestBuildSubmitManifestAcceptsFileAtSubmissionLimit(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "output", "limit.bin")
	if err := os.WriteFile(path, make([]byte, maxSubmissionFileSize), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: SnapshotSchemaVersion, Files: []FileManifest{{Identity: "delivery-1", RelativePath: "output/limit.bin", Role: MaterialOutputWritable}}}
	_, files, err := buildSubmitManifest(TaskRecord{Key: key, Directory: dir, Manifest: &manifest}, Verification{}, []DeliverableFileBinding{{DeliverableID: "delivery-1", File: "output/limit.bin"}})
	if err != nil || len(files) != 1 || int64(len(files[0].Content)) != maxSubmissionFileSize {
		t.Fatalf("files=%d err=%v", len(files), err)
	}
}

func TestUntrackedTaskFilesFindsRootDeliverableCandidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chinese_chess.html"), []byte("<html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input", "reference.md"), []byte("reference"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := untrackedTaskFiles(TaskRecord{Directory: dir})
	if len(got) != 1 || got[0] != "chinese_chess.html" {
		t.Fatalf("candidates = %#v", got)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestConfirmRejectsPreviewAfterLocalContentChanges(t *testing.T) {
	var confirmCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/tasks/node/worker/submit/preview":
			request := decodeBody[SubmitPreviewRequest](t, r)
			_ = json.NewEncoder(w).Encode(Preview{ID: "preview-1", Kind: "submit", Status: "previewed", TaskVersion: request.TaskVersion, ContextVersion: request.ContextVersion, Attempt: request.Attempt, MaterialDigest: request.MaterialDigest, ContentDigest: request.ContentDigest, ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/task-operations/preview-1/confirm":
			confirmCalls.Add(1)
			_, _ = w.Write([]byte(`{"id":"operation-1","status":"accepted"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	store, key, output := seedOperationTask(t)
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	svc.now = func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }
	preview, err := svc.PreviewSubmit(context.Background(), key)
	if err != nil {
		t.Fatalf("PreviewSubmit: %v", err)
	}
	if preview.ID != "preview-1" {
		t.Fatalf("preview = %+v", preview)
	}
	if err := os.WriteFile(output, []byte("changed again"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ConfirmOperation(context.Background(), key, preview.ID)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "preview_stale" {
		t.Fatalf("ConfirmOperation error = %#v", err)
	}
	if confirmCalls.Load() != 0 {
		t.Fatalf("confirm calls = %d", confirmCalls.Load())
	}
}

func TestConfirmOperationReturnsAlreadyCompletedWithoutCloudRequest(t *testing.T) {
	var confirmCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		confirmCalls.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer srv.Close()
	store, key, _ := seedOperationTask(t)
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	record.Preview = &Preview{ID: "preview-1", MaterialDigest: effectiveMaterialDigest(record), ContentDigest: verification.ContentDigest}
	record.Operation = &Operation{ID: "operation-1", PreviewID: "preview-1", Status: OperationCompleted, Kind: "submit"}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}

	operation, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).ConfirmOperation(context.Background(), key, "preview-1")
	if err != nil {
		t.Fatalf("ConfirmOperation: %v", err)
	}
	if operation.Outcome != OutcomeAlreadyCompleted {
		t.Fatalf("outcome = %q, want %q", operation.Outcome, OutcomeAlreadyCompleted)
	}
	if confirmCalls.Load() != 0 {
		t.Fatalf("confirm calls = %d, want 0", confirmCalls.Load())
	}
}

func TestConfirmOperationRecoversCompletedResponseAfterTransientFailure(t *testing.T) {
	var confirmCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/member/task-operations/preview-1/confirm" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if confirmCalls.Add(1) == 1 {
			http.Error(w, "response lost", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(Operation{ID: "operation-1", PreviewID: "preview-1", Status: OperationCompleted, Kind: "submit"})
	}))
	defer srv.Close()
	store, key, _ := seedOperationTask(t)
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	record.Preview = &Preview{ID: "preview-1", MaterialDigest: effectiveMaterialDigest(record), ContentDigest: verification.ContentDigest}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}

	operation, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).ConfirmOperation(context.Background(), key, "preview-1")
	if err != nil {
		t.Fatalf("ConfirmOperation: %v", err)
	}
	if operation.Outcome != OutcomeRecovered {
		t.Fatalf("outcome = %q, want %q", operation.Outcome, OutcomeRecovered)
	}
	if confirmCalls.Load() != 2 {
		t.Fatalf("confirm calls = %d, want 2", confirmCalls.Load())
	}
	record, _, err = store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Ended {
		t.Fatal("ended = false, want true after recovered completion")
	}
}

func TestPreviewReviewRequiresReasonForReject(t *testing.T) {
	store, _, _ := seedOperationTask(t)
	critic, _ := ParseTaskKey("cloud/ws/node/critic")
	worker, _, _ := store.LoadTask(TaskKey{CloudInstanceID: "cloud", WorkspaceID: "ws", NodeRunID: "node", Role: RoleWorker})
	worker.Key = critic
	_ = store.SaveTask(worker)
	_, err := NewService(store, nil).PreviewReview(context.Background(), critic, "reject", "")
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_arguments" {
		t.Fatalf("PreviewReview error = %#v", err)
	}
}

func TestRecoverOperationUsesOriginalOperationAndMarksCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/member/task-operations/operation-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"operation-1","status":"completed","kind":"submit","publish_plan":[]}`))
	}))
	defer srv.Close()
	store, key, _ := seedOperationTask(t)
	record, _, _ := store.LoadTask(key)
	record.Operation = &Operation{ID: "operation-1", PreviewID: "preview-1", Status: OperationAccepted, Kind: "submit"}
	_ = store.SaveTask(record)
	operation, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).RecoverOperation(context.Background(), key)
	if err != nil {
		t.Fatalf("RecoverOperation: %v", err)
	}
	if operation.Status != OperationCompleted {
		t.Fatalf("operation = %+v", operation)
	}
	if operation.PreviewID != "preview-1" {
		t.Fatalf("preview id = %q, want preserved preview-1", operation.PreviewID)
	}
	record, _, _ = store.LoadTask(key)
	if !record.Ended || record.Operation == nil || record.Operation.PreviewID != "preview-1" {
		t.Fatalf("record = %+v", record)
	}
}

func TestParseRemoteVersionRejectsTrailingInput(t *testing.T) {
	if _, _, _, err := parseRemoteVersion("1:2:3garbage"); err == nil {
		t.Fatal("parseRemoteVersion accepted trailing input")
	}
}

func TestRecoverOperationSavesRepreviewRequiredWithoutPublishingOrReporting(t *testing.T) {
	var reportCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/member/task-operations/operation-1":
			_ = json.NewEncoder(w).Encode(Operation{
				ID: "operation-1", PreviewID: "preview-1", Status: OperationRepreviewRequired, Kind: "submit",
				PublishSteps: []RepoPublishPlan{{RepositoryIdentity: "repo", ExpectedRef: "refs/heads/tasks/result"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/task-operations/operation-1/report":
			reportCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	store, key, _ := seedOperationTask(t)
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	record.Operation = &Operation{ID: "operation-1", PreviewID: "preview-1", Status: OperationAccepted, Kind: "submit"}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	var publishCalls atomic.Int32
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	svc.publisher = newGitPublisherWithRunner(func(context.Context, string, ...string) (string, error) {
		publishCalls.Add(1)
		return "", nil
	})

	operation, err := svc.RecoverOperation(context.Background(), key)
	if err != nil {
		t.Fatalf("RecoverOperation: %v", err)
	}
	if operation.Status != OperationRepreviewRequired {
		t.Fatalf("operation status = %q, want %q", operation.Status, OperationRepreviewRequired)
	}
	record, _, err = store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	if record.Operation == nil || record.Operation.Status != OperationRepreviewRequired || record.Ended {
		t.Fatalf("record = %+v, want saved repreview-required operation with ended=false", record)
	}
	if publishCalls.Load() != 0 || reportCalls.Load() != 0 {
		t.Fatalf("publish calls = %d, report calls = %d, want 0/0", publishCalls.Load(), reportCalls.Load())
	}
}

func TestRemoteRefChangeReportsOriginalOperationBeforeReturning(t *testing.T) {
	var report StepReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/member/task-operations/operation-1/report" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		report = decodeBody[StepReport](t, r)
		_ = json.NewEncoder(w).Encode(Operation{ID: "operation-1", Status: OperationRepreviewRequired})
	}))
	defer srv.Close()
	store, key, _ := seedOperationTask(t)
	record, _, _ := store.LoadTask(key)
	repoDir := filepath.Join(record.Directory, "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	record.Manifest.Repositories = []RepositoryManifest{{Identity: "repo", RelativePath: "repo", TargetRef: "refs/heads/tasks/result"}}
	record.Operation = &Operation{ID: "operation-1", Status: OperationAccepted, PublishSteps: []RepoPublishPlan{{RepositoryIdentity: "repo", ExpectedRef: "refs/heads/tasks/result", BeforeSHA: "before", HeadSHA: "head"}}}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	svc.publisher = newGitPublisherWithRunner(func(context.Context, string, ...string) (string, error) {
		return "other\trefs/heads/tasks/result", nil
	})
	_, err := svc.executeOperation(context.Background(), record)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "remote_ref_changed" {
		t.Fatalf("error = %#v", err)
	}
	if report.RepositoryIdentity != "repo" || report.ErrorCode != "remote_ref_changed" || report.Status != RepoStepConflict {
		t.Fatalf("report = %+v", report)
	}
}

func seedOperationTask(t *testing.T) (*Store, TaskKey, string) {
	t.Helper()
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	output := filepath.Join(dir, "output", "result.md")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := BuildManifest(dir, []MaterialSource{{Identity: "result", SourceVersion: "v1", RelativePath: "output/result.md", Role: MaterialOutputWritable}})
	if err := os.WriteFile(output, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := TaskRecord{Key: key, RemoteVersion: "1:1:1", Attempt: 1, Directory: dir, Prepared: true, Manifest: &manifest}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	return store, key, output
}

func decodeBody[T any](t *testing.T, r *http.Request) T {
	t.Helper()
	var value T
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
