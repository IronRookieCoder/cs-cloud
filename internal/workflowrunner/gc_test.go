package workflowrunner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner/execenv"
)

// newGCDriver builds a minimal *Driver whose client points at a mock multica
// server. Task dirs are created under <root>/<wsID>/tasks/<name>.
func newGCDriver(t *testing.T, handler http.Handler) *Driver {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	root := t.TempDir()
	return &Driver{
		cfg: workflow.Config{
			WorkspacesRoot:     root,
			GCEnabled:          true,
			GCInterval:         time.Hour,
			GCTTL:              5 * 24 * time.Hour,
			GCOrphanTTL:        30 * 24 * time.Hour,
			GCArtifactTTL:      12 * time.Hour,
			GCArtifactPatterns: []string{"node_modules", ".next", ".turbo"},
		},
		client:  NewClient(srv.URL, "", tokenProvider("test-token")),
		running: map[string]*taskRecord{},
	}
}

// createTaskDir creates <root>/<wsID>/<name> with optional GC metadata.
func createTaskDir(t *testing.T, root, wsID, dirName string, meta *execenv.GCMeta) string {
	t.Helper()
	taskDir := filepath.Join(root, wsID, "tasks", dirName)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		if err := execenv.WriteGCMeta(taskDir, *meta); err != nil {
			t.Fatal(err)
		}
	}
	return taskDir
}

func TestShouldCleanTaskDir_DoneIssueOverTTL(t *testing.T) {
	issueID := "11111111-1111-1111-1111-111111111111"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "done", "updated_at": time.Now().Add(-10 * 24 * time.Hour)})
	})
	d := newGCDriver(t, mux)
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task1", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-10 * 24 * time.Hour),
	})
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionClean {
		t.Fatalf("expected gcActionClean, got %d", got)
	}
}

func TestShouldCleanTaskDir_OpenIssueSkipped(t *testing.T) {
	issueID := "22222222-2222-2222-2222-222222222222"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "in_progress", "updated_at": time.Now().Add(-30 * 24 * time.Hour)})
	})
	d := newGCDriver(t, mux)
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task2", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1", CompletedAt: time.Now(),
	})
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionSkip {
		t.Fatalf("expected gcActionSkip for open issue, got %d", got)
	}
}

func TestShouldCleanTaskDir_NoMetaOldOrphan(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	d.cfg.GCOrphanTTL = 0 // treat all orphans as expired
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task3", nil)
	backDate(t, taskDir, time.Hour)
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionOrphan {
		t.Fatalf("expected gcActionOrphan, got %d", got)
	}
}

func TestShouldCleanTaskDir_NoMetaRecentSkipped(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task4", nil)
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionSkip {
		t.Fatalf("expected gcActionSkip for recent orphan, got %d", got)
	}
}

func TestShouldCleanTaskDir_Issue404OldOrphan(t *testing.T) {
	issueID := "33333333-3333-3333-3333-333333333333"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	d := newGCDriver(t, mux)
	d.cfg.GCOrphanTTL = 0
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task5", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1", CompletedAt: time.Now(),
	})
	backDate(t, taskDir, time.Hour)
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionOrphan {
		t.Fatalf("expected gcActionOrphan for unreachable issue past TTL, got %d", got)
	}
}

func TestShouldCleanTaskDir_APIErrorSkipped(t *testing.T) {
	issueID := "44444444-4444-4444-4444-444444444444"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	d := newGCDriver(t, mux)
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "task6", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1", CompletedAt: time.Now(),
	})
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionSkip {
		t.Fatalf("expected gcActionSkip on API error, got %d", got)
	}
}

func TestShouldCleanTaskDir_ActiveEnvRootSkips(t *testing.T) {
	issueID := "55555555-5555-5555-5555-555555555555"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "done", "updated_at": time.Now().Add(-30 * 24 * time.Hour)})
	})
	d := newGCDriver(t, mux)
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "active", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-30 * 24 * time.Hour),
	})
	d.running["t-active"] = &taskRecord{taskRoot: taskDir}
	defer delete(d.running, "t-active")

	// Even though done+stale would normally clean, the active guard overrides.
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionSkip {
		t.Fatalf("expected gcActionSkip while task is active, got %d", got)
	}
}

func TestShouldCleanTaskDir_OpenIssueArtifactCleanup(t *testing.T) {
	issueID := "66666666-6666-6666-6666-666666666666"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "in_progress", "updated_at": time.Now()})
	})
	d := newGCDriver(t, mux)
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "open-task", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-24 * time.Hour),
	})
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionCleanArtifacts {
		t.Fatalf("expected gcActionCleanArtifacts, got %d", got)
	}
}

func TestShouldCleanTaskDir_ArtifactTTLDisabled(t *testing.T) {
	issueID := "77777777-7777-7777-7777-777777777777"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "in_progress", "updated_at": time.Now()})
	})
	d := newGCDriver(t, mux)
	d.cfg.GCArtifactTTL = 0
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "no-art", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-100 * 24 * time.Hour),
	})
	if got := d.shouldCleanTaskDir(taskDir); got != gcActionSkip {
		t.Fatalf("expected gcActionSkip when artifact GC disabled, got %d", got)
	}
}

func TestCleanTaskDir_RemovesDirectory(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "doomed", nil)
	if _, err := os.Stat(taskDir); err != nil {
		t.Fatal("task dir should exist before cleanup")
	}
	d.cleanTaskDir(taskDir)
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatal("task dir should be removed after cleanup")
	}
}

func TestGcWorkspace_CleansEmptyTasksDir(t *testing.T) {
	issueID := "88888888-8888-8888-8888-888888888888"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, issueID), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "done", "updated_at": time.Now().Add(-10 * 24 * time.Hour)})
	})
	d := newGCDriver(t, mux)
	wsDir := filepath.Join(d.cfg.WorkspacesRoot, "ws-empty")
	createTaskDir(t, d.cfg.WorkspacesRoot, "ws-empty", "only-task", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws-empty",
		CompletedAt: time.Now().Add(-10 * 24 * time.Hour),
	})
	d.gcWorkspace(wsDir, &gcStats{byPattern: map[string]int{}})
	tasksDir := filepath.Join(wsDir, "tasks")
	if _, err := os.Stat(tasksDir); !os.IsNotExist(err) {
		t.Fatalf("empty tasks/ dir should be removed, got %v", err)
	}
}

func TestCleanTaskArtifacts_RemovesOnlyMatchedDirs(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	taskDir := t.TempDir()

	mustMkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(taskDir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(rel, content string) {
		p := filepath.Join(taskDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustMkdir("workdir/repo/src")
	mustWrite("workdir/repo/src/index.ts", "console.log('hi')")
	mustMkdir("workdir/repo/.git/objects")
	mustWrite("workdir/repo/.git/objects/pack", "binary")
	mustMkdir("workdir/repo/node_modules/lodash")
	mustWrite("workdir/repo/node_modules/lodash/index.js", "module.exports = {}")
	mustMkdir("workdir/repo/.next/cache")
	mustWrite("workdir/repo/.next/cache/page.html", "<html></html>")
	mustMkdir("workdir/repo/.turbo")
	mustWrite("workdir/repo/.turbo/log", "trace")
	mustMkdir("workdir/repo/dist") // not in patterns — preserved
	mustWrite("workdir/repo/dist/main.js", "compiled")

	removed, bytes, perPattern := d.cleanTaskArtifacts(taskDir, []string{"node_modules", ".next", ".turbo"})
	if removed != 3 {
		t.Fatalf("expected 3 artifact dirs removed, got %d", removed)
	}
	if bytes <= 0 {
		t.Fatalf("expected non-zero bytes reclaimed, got %d", bytes)
	}
	if perPattern["node_modules"] != 1 || perPattern[".next"] != 1 || perPattern[".turbo"] != 1 {
		t.Fatalf("unexpected per-pattern counts: %+v", perPattern)
	}
	for _, rel := range []string{"workdir/repo/src/index.ts", "workdir/repo/.git/objects/pack", "workdir/repo/dist/main.js"} {
		if _, err := os.Stat(filepath.Join(taskDir, rel)); err != nil {
			t.Errorf("expected %s preserved, got %v", rel, err)
		}
	}
	for _, rel := range []string{"workdir/repo/node_modules", "workdir/repo/.next", "workdir/repo/.turbo"} {
		if _, err := os.Stat(filepath.Join(taskDir, rel)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", rel, err)
		}
	}
}

func TestCleanTaskArtifacts_RejectsPatternsWithSeparators(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	taskDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(taskDir, "workdir", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	removed, _, _ := d.cleanTaskArtifacts(taskDir, []string{"workdir/node_modules", "../etc"})
	if removed != 0 {
		t.Fatalf("expected 0 removals from separator-bearing patterns, got %d", removed)
	}
}

func TestCleanTaskArtifacts_DoesNotFollowSymlinks(t *testing.T) {
	d := newGCDriver(t, http.NewServeMux())
	taskDir := t.TempDir()
	outside := t.TempDir()
	keepFile := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(taskDir, "workdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(taskDir, "workdir", "node_modules")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	removed, _, _ := d.cleanTaskArtifacts(taskDir, []string{"node_modules"})
	if removed != 0 {
		t.Fatalf("expected 0 removals (symlinked node_modules), got %d", removed)
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("symlinked target was deleted: %v", err)
	}
}

func TestIsBareRepo(t *testing.T) {
	t.Run("valid bare repo", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main"), 0o644)
		os.MkdirAll(filepath.Join(dir, "objects"), 0o755)
		if !isBareRepo(dir) {
			t.Fatal("expected isBareRepo=true for dir with HEAD + objects/")
		}
	})
	t.Run("HEAD only", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main"), 0o644)
		if isBareRepo(dir) {
			t.Fatal("expected isBareRepo=false for dir with only HEAD")
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		dir := t.TempDir()
		if isBareRepo(dir) {
			t.Fatal("expected isBareRepo=false for empty dir")
		}
	})
}

// TestShouldCleanTaskDir_KindDispatch covers the GCMeta kinds (including the
// cs-cloud workflow_node_run) across terminal / non-terminal / 404 axes.
func TestShouldCleanTaskDir_KindDispatch(t *testing.T) {
	const (
		issueID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
		chatID     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb01"
		runID      = "cccccccc-cccc-cccc-cccc-cccccccccc01"
		quickTask  = "dddddddd-dddd-dddd-dddd-dddddddddd01"
		nodeRunID  = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeee01"
		legacyMeta = "ffffffff-ffff-ffff-ffff-ffffffffff01"
	)

	now := time.Now()
	overTTL := now.Add(-10 * 24 * time.Hour)
	withinTTL := now.Add(-1 * time.Hour)

	type serverResp struct {
		path   string
		status int
		body   map[string]any
	}
	cases := []struct {
		name    string
		meta    *execenv.GCMeta
		servers []serverResp
		want    gcAction
	}{
		// chat
		{name: "chat active session — never reclaimed",
			meta: &execenv.GCMeta{Kind: execenv.GCKindChat, ChatSessionID: chatID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaChatSessionGCCheckEndpoint, chatID), body: map[string]any{"status": "active", "updated_at": overTTL}}},
			want: gcActionSkip},
		{name: "chat archived over TTL — clean",
			meta: &execenv.GCMeta{Kind: execenv.GCKindChat, ChatSessionID: chatID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaChatSessionGCCheckEndpoint, chatID), body: map[string]any{"status": "archived", "updated_at": overTTL}}},
			want: gcActionClean},
		{name: "chat 404 — hard-deleted, clean immediately",
			meta: &execenv.GCMeta{Kind: execenv.GCKindChat, ChatSessionID: chatID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaChatSessionGCCheckEndpoint, chatID), status: http.StatusNotFound}},
			want: gcActionClean},

		// autopilot
		{name: "autopilot completed over TTL — clean",
			meta: &execenv.GCMeta{Kind: execenv.GCKindAutopilotRun, AutopilotRunID: runID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaAutopilotRunGCCheckEndpoint, runID), body: map[string]any{"status": "completed", "completed_at": overTTL}}},
			want: gcActionClean},
		{name: "autopilot running — skip",
			meta: &execenv.GCMeta{Kind: execenv.GCKindAutopilotRun, AutopilotRunID: runID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaAutopilotRunGCCheckEndpoint, runID), body: map[string]any{"status": "running"}}},
			want: gcActionSkip},

		// quick-create
		{name: "quick_create completed — clean immediately",
			meta: &execenv.GCMeta{Kind: execenv.GCKindQuickCreate, TaskID: quickTask, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaTaskGCCheckEndpoint, quickTask), body: map[string]any{"status": "completed", "completed_at": withinTTL}}},
			want: gcActionClean},
		{name: "quick_create running — skip",
			meta: &execenv.GCMeta{Kind: execenv.GCKindQuickCreate, TaskID: quickTask, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaTaskGCCheckEndpoint, quickTask), body: map[string]any{"status": "running"}}},
			want: gcActionSkip},

		// workflow node-run (cs-cloud)
		{name: "node_run completed over TTL — clean",
			meta: &execenv.GCMeta{Kind: execenv.GCKindWorkflowNodeRun, NodeRunID: nodeRunID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaWorkflowNodeRunGCCheckEndpoint, nodeRunID), body: map[string]any{"status": "completed", "completed_at": overTTL}}},
			want: gcActionClean},
		{name: "node_run working — skip",
			meta: &execenv.GCMeta{Kind: execenv.GCKindWorkflowNodeRun, NodeRunID: nodeRunID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaWorkflowNodeRunGCCheckEndpoint, nodeRunID), body: map[string]any{"status": "working"}}},
			want: gcActionSkip},
		{name: "node_run 404 old orphan — orphanByMTime",
			meta: &execenv.GCMeta{Kind: execenv.GCKindWorkflowNodeRun, NodeRunID: nodeRunID, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaWorkflowNodeRunGCCheckEndpoint, nodeRunID), status: http.StatusNotFound}},
			want: gcActionOrphan},

		// issue path (legacy no-kind normalization is covered by the execenv
		// package's ReadGCMeta test; here we just exercise the issue decision).
		{name: "issue done over TTL — clean",
			meta: &execenv.GCMeta{Kind: execenv.GCKindIssue, IssueID: legacyMeta, WorkspaceID: "ws"},
			servers: []serverResp{{path: fmt.Sprintf(workflow.MulticaIssueGCCheckEndpoint, legacyMeta), body: map[string]any{"status": "done", "updated_at": overTTL}}},
			want: gcActionClean},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			for _, s := range tc.servers {
				resp := s
				mux.HandleFunc(resp.path, func(w http.ResponseWriter, r *http.Request) {
					if resp.status != 0 {
						w.WriteHeader(resp.status)
						return
					}
					writeJSON(w, resp.body)
				})
			}
			d := newGCDriver(t, mux)
			// node_run 404 orphan case needs GCOrphanTTL=0 so mtime path fires.
			if tc.want == gcActionOrphan {
				d.cfg.GCOrphanTTL = 0
			}
			taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws", tc.name, tc.meta)
			if tc.want == gcActionOrphan {
				backDate(t, taskDir, time.Hour) // Windows mtime-resolution robustness
			}
			if got := d.shouldCleanTaskDir(taskDir); got != tc.want {
				t.Fatalf("kind dispatch %q: want %d, got %d", tc.name, tc.want, got)
			}
		})
	}
}

// writeJSON is a tiny test helper that encodes a value as JSON with 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// backDate sets a directory's mtime into the past so GCOrphanTTL=0 tests fire
// reliably on platforms with coarse filesystem timestamp resolution (Windows
// NTFS), where a freshly-created dir's time.Since(mtime) can be ≤ 0.
func backDate(t *testing.T, dir string, ago time.Duration) {
	t.Helper()
	past := time.Now().Add(-ago)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}
}
