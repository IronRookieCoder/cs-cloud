package membertask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMemberTaskMaterializeAndSubmitIntegration exercises the local member-task
// flow against a deterministic cloud HTTP harness. The confirm response models
// the server-side finalized operation and workflow transition.
func TestMemberTaskMaterializeAndSubmitIntegration(t *testing.T) {
	key := TaskKey{CloudInstanceID: "cloud", WorkspaceID: "ws", NodeRunID: "node", Role: RoleWorker}
	predecessor := "predecessor result: approved\n"
	predecessorSum := sha256.Sum256([]byte(predecessor))
	var mu sync.Mutex
	var previewRequest SubmitPreviewRequest
	var previewCalls, confirmCalls int

	contextValue := RemoteTaskContext{
		RemoteTask:           RemoteTask{Ref: key, Attempt: 1, TaskVersion: 2, ContextVersion: 3, CloudStatus: "assigned"},
		PrepareAllowed:       true,
		Providers:            []string{"gitea"},
		PredecessorResults:   []RemoteDeliverable{{ID: "predecessor-1", Title: "Prior result", Content: predecessor, Version: "v1", SHA256: hex.EncodeToString(predecessorSum[:])}},
		RequiredDeliverables: []RemoteDeliverable{{ID: "chinese_chess.html", Title: "Chinese chess page", Required: true, Content: "starter", Version: "v1"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/member/tasks/node/worker/context":
			_ = json.NewEncoder(w).Encode(contextValue)
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/tasks/node/worker/submit/preview":
			var decoded SubmitPreviewRequest
			if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			previewRequest = decoded
			previewCalls++
			requestSnapshot := previewRequest
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(Preview{ID: "preview-1", Kind: "submit", Status: "previewed", Attempt: 1, TaskVersion: 2, ContextVersion: 3, MaterialDigest: requestSnapshot.MaterialDigest, ContentDigest: requestSnapshot.ContentDigest, PublishPlan: json.RawMessage(`{"files":["chinese_chess.html"]}`), ExpiresAt: time.Now().Add(time.Hour)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/task-operations/preview-1/confirm":
			mu.Lock()
			confirmCalls++
			requestSnapshot := previewRequest
			mu.Unlock()
			if len(requestSnapshot.Files) != 1 || requestSnapshot.Files[0].Identity != "chinese_chess.html" {
				http.Error(w, "required deliverable missing", http.StatusUnprocessableEntity)
				return
			}
			_ = json.NewEncoder(w).Encode(Operation{ID: "operation-1", PreviewID: "preview-1", Kind: "submit", Status: OperationCompleted, PublishPlan: json.RawMessage(`{"files":["chinese_chess.html"]}`)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	task, err := svc.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if task.Context == nil || len(task.Context.PredecessorResults) != 1 {
		t.Fatalf("context predecessor results = %#v", task.Context)
	}
	transition, err := svc.Handle(context.Background(), key, PrepareOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if transition.Manifest == nil {
		t.Fatal("Handle returned no manifest")
	}
	if got := transition.Manifest.Files[0].SHA256; got != hex.EncodeToString(predecessorSum[:]) {
		t.Fatalf("predecessor digest = %s, want %s", got, hex.EncodeToString(predecessorSum[:]))
	}
	var output string
	for _, file := range transition.Manifest.Files {
		if file.Identity == "chinese_chess.html" {
			output = file.RelativePath
			break
		}
	}
	if output == "" {
		t.Fatal("required deliverable missing from manifest")
	}
	outputPath := filepath.Join(transition.Directory, filepath.FromSlash(output))
	if err := os.WriteFile(outputPath, []byte("<html>chess</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewSubmit(context.Background(), key, []DeliverableFileBinding{{DeliverableID: "chinese_chess.html", File: output}})
	if err != nil {
		t.Fatalf("PreviewSubmit: %v", err)
	}
	if preview.PublishPlan == nil || len(preview.PublishPlan) == 0 {
		t.Fatal("publish plan is empty")
	}
	mu.Lock()
	requestSnapshot := previewRequest
	mu.Unlock()
	if len(requestSnapshot.Files) != 1 || requestSnapshot.Files[0].Identity != "chinese_chess.html" {
		t.Fatalf("preview files = %#v", previewRequest.Files)
	}
	if requestSnapshot.Files[0].Name != filepath.Base(output) {
		t.Fatalf("file name = %q, want %q", requestSnapshot.Files[0].Name, filepath.Base(output))
	}
	if requestSnapshot.Files[0].RelativePath != output {
		t.Fatalf("relative path = %q, want %q", requestSnapshot.Files[0].RelativePath, output)
	}
	operation, err := svc.ConfirmOperation(context.Background(), key, preview.ID)
	if err != nil {
		t.Fatalf("ConfirmOperation: %v", err)
	}
	if operation.Status != OperationCompleted {
		t.Fatalf("operation status = %s", operation.Status)
	}
	if _, err := svc.ConfirmOperation(context.Background(), key, preview.ID); err != nil {
		t.Fatalf("repeat ConfirmOperation: %v", err)
	}
	mu.Lock()
	gotPreviewCalls, gotConfirmCalls := previewCalls, confirmCalls
	mu.Unlock()
	if gotPreviewCalls != 1 || gotConfirmCalls != 1 {
		t.Fatalf("cloud calls preview=%d confirm=%d, want 1/1", gotPreviewCalls, gotConfirmCalls)
	}
	record, found, err := store.LoadTask(key)
	if err != nil || !found {
		t.Fatalf("LoadTask: found=%v err=%v", found, err)
	}
	if record.Operation == nil || record.Operation.Status != OperationCompleted || !record.Ended {
		t.Fatalf("stored record = %+v", record)
	}
}
