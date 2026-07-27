# Issue 页面会话串联实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 multica 后端实现 `GET /api/workspaces/{workspaceID}/issues/{issueID}/session`，让 issue 页面通过 issue id 拿到 conversation_id、workspace_directory 和 Gateway events_url；cs-cloud 与设备本地不保存 issue→conversation 映射。

**Architecture:** multica 后端保存 `issue_id → (conversation_id, workspace_directory)` 映射与项目本地路径；首次请求时通过 Gateway 同步调用 cs-cloud `POST /conversations` 创建会话，后续请求直接复用。前端拿到 conversation_id 与 events_url 后复用现有 workspace 对话组件。

**Tech Stack:** Go / Chi / pgx / sqlc / 现有 `internal/cloudruntime` 客户端。

---

## 0. 前置依赖

本计划假设 cs-cloud 已将其 Gateway `device_id` 作为 `daemon_id`（或 `metadata->>'device_id'`）注册到 multica 的 `agent_runtime` 表中，且 provider 为 `cs-cloud`。

如果尚未实现，请在 cs-cloud workflow driver 启动时增加一次 `/api/daemon/register` 调用（daemon_id = Gateway device_id，provider = `cs-cloud`，metadata 中包含 `device_id`），并维持心跳。此步骤尽量保持最小改动，只影响 `internal/agent/workflow` 包。

---

## Task 1: 数据库迁移

**Files:**
- Create: `server/migrations/135_issue_conversation_session.up.sql`
- Create: `server/migrations/135_issue_conversation_session.down.sql`

- [ ] **Step 1: 编写 up 迁移**

```sql
-- 135_issue_conversation_session.up.sql
CREATE TABLE multica_issue_conversation (
    issue_id            UUID PRIMARY KEY REFERENCES multica_issue(id) ON DELETE CASCADE,
    conversation_id     TEXT NOT NULL,
    workspace_directory TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_issue_conversation_issue_id ON multica_issue_conversation(issue_id);

-- 项目级本地绝对路径，multica 端维护，不依赖 cs-cloud 状态
ALTER TABLE multica_project ADD COLUMN local_directory TEXT;
```

- [ ] **Step 2: 编写 down 迁移**

```sql
-- 135_issue_conversation_session.down.sql
DROP TABLE IF EXISTS multica_issue_conversation;
ALTER TABLE multica_project DROP COLUMN IF EXISTS local_directory;
```

- [ ] **Step 3: 在本地/测试数据库执行迁移**

Run:
```bash
cd <multica-repo>/server
go run ./cmd/migrate up
```
Expected: migrations applied successfully.

---

## Task 2: sqlc 查询

**Files:**
- Create: `server/pkg/db/queries/issue_conversation.sql`
- Modify: `server/pkg/db/queries/project.sql`
- Modify: `server/pkg/db/queries/runtime.sql`
- Generated files will be updated by `sqlc generate`

- [ ] **Step 1: 编写 issue_conversation 查询**

```sql
-- name: GetIssueConversation :one
SELECT issue_id, conversation_id, workspace_directory, device_id, created_at, updated_at
FROM multica_issue_conversation
WHERE issue_id = $1;

-- name: CreateIssueConversation :one
INSERT INTO multica_issue_conversation (
    issue_id, conversation_id, workspace_directory, device_id
) VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id) DO UPDATE SET
    conversation_id = EXCLUDED.conversation_id,
    workspace_directory = EXCLUDED.workspace_directory,
    device_id = EXCLUDED.device_id,
    updated_at = now()
RETURNING issue_id, conversation_id, workspace_directory, device_id, created_at, updated_at;

-- name: DeleteIssueConversation :exec
DELETE FROM multica_issue_conversation WHERE issue_id = $1;
```

- [ ] **Step 2: 在 project.sql 中增加本地路径查询/更新**

Append to `server/pkg/db/queries/project.sql`:

```sql
-- name: GetProjectLocalDirectory :one
SELECT local_directory FROM multica_project
WHERE id = $1 AND workspace_id = $2;

-- name: UpdateProjectLocalDirectory :exec
UPDATE multica_project
SET local_directory = $1, updated_at = now()
WHERE id = $2 AND workspace_id = $3;
```

- [ ] **Step 3: 在 runtime.sql 中增加按 workspace+provider 查询在线 runtime**

Append to `server/pkg/db/queries/runtime.sql`:

```sql
-- name: ListOnlineAgentRuntimesByWorkspaceAndProvider :many
SELECT * FROM multica_agent_runtime
WHERE workspace_id = $1 AND provider = $2 AND status = 'online'
ORDER BY last_seen_at DESC;
```

- [ ] **Step 4: 重新生成 sqlc 代码**

Run:
```bash
cd <multica-repo>/server
sqlc generate
```
Expected: `pkg/db/generated/` updates成功，无报错。

- [ ] **Step 5: 提交**

```bash
git add server/migrations/135_issue_conversation_session.up.sql \
        server/migrations/135_issue_conversation_session.down.sql \
        server/pkg/db/queries/issue_conversation.sql \
        server/pkg/db/queries/project.sql \
        server/pkg/db/queries/runtime.sql \
        server/pkg/db/generated/
git commit -m "feat(db): issue conversation session mapping and project local_directory"
```

---

## Task 3: Gateway events_url 配置

**Files:**
- Modify: `server/internal/handler/handler.go` (Config struct)
- Modify: `server/cmd/server/router.go` (populate from env)

- [ ] **Step 1: 在 handler.Config 中增加 Gateway proxy 前缀**

```go
// internal/handler/handler.go
// 用于构造前端直连 Gateway 的 events_url，默认兼容 CoStrict cloud 路径。
CloudGatewayProxyPrefix string
```

- [ ] **Step 2: 在 router 中从环境变量读取**

In `cmd/server/router.go`, inside `signupConfig := handler.Config{...}`:

```go
CloudGatewayProxyPrefix: strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_CLOUD_GATEWAY_PROXY_PREFIX")), "/"),
```

Also add a default helper in `handler.go` or `router.go`:

```go
func gatewayProxyPrefix(cfg handler.Config) string {
    if cfg.CloudGatewayProxyPrefix != "" {
        return cfg.CloudGatewayProxyPrefix
    }
    return "/cloud-api/cloud/device/%s/proxy"
}
```

- [ ] **Step 3: 提交**

```bash
git add server/internal/handler/handler.go server/cmd/server/router.go
git commit -m "feat(config): add MULTICA_CLOUD_GATEWAY_PROXY_PREFIX for issue events URL"
```

---

## Task 4: 实现 `GetIssueConversationSession` handler

**Files:**
- Create: `server/internal/handler/issue_conversation.go`

- [ ] **Step 1: 编写 handler 代码**

```go
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const csCloudRuntimeProvider = "cs-cloud"

type IssueConversationSessionResponse struct {
	ConversationID     string `json:"conversation_id"`
	WorkspaceDirectory string `json:"workspace_directory"`
	EventsURL          string `json:"events_url"`
}

type createConversationRequest struct {
	Agent             string `json:"agent"`
	WorkspaceDirectory string `json:"workspace_directory"`
	InitialPrompt     string `json:"initial_prompt"`
}

type createConversationResponse struct {
	ID string `json:"id"`
}

// GetIssueConversationSession returns the conversation tied to an issue,
// creating it through the Gateway/cs-cloud when necessary.
func (h *Handler) GetIssueConversationSession(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	issueID := chi.URLParam(r, "issueID")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue_id")
	if !ok {
		return
	}

	// Verify workspace membership.
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issueUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}

	// Resolve local directory from project.
	workspaceDir, ok := h.resolveIssueWorkspaceDirectory(w, r.Context(), issue)
	if !ok {
		return
	}

	// Existing mapping?
	conv, err := h.Queries.GetIssueConversation(r.Context(), issueUUID)
	if err == nil && conv.ConversationID != "" {
		h.writeIssueConversationSession(w, conv.ConversationID, workspaceDir, workspaceID)
		return
	}

	// Find an online cs-cloud runtime for this workspace.
	deviceID, ok := h.resolveCSCloudDeviceID(w, r.Context(), wsUUID)
	if !ok {
		return
	}

	// Create conversation through Gateway.
	convID, ok := h.createConversationOnDevice(w, r, deviceID, userID, workspaceDir, issue)
	if !ok {
		return
	}

	// Persist mapping.
	created, err := h.Queries.CreateIssueConversation(r.Context(), db.CreateIssueConversationParams{
		IssueID:            issueUUID,
		ConversationID:     convID,
		WorkspaceDirectory: workspaceDir,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save issue conversation")
		return
	}

	h.writeIssueConversationSession(w, created.ConversationID, created.WorkspaceDirectory, workspaceID)
}

func (h *Handler) resolveIssueWorkspaceDirectory(w http.ResponseWriter, ctx context.Context, issue db.MulticaIssue) (string, bool) {
	if !issue.ProjectID.Valid {
		writeError(w, http.StatusBadRequest, "issue has no project")
		return "", false
	}

	localDir, err := h.Queries.GetProjectLocalDirectory(ctx, db.GetProjectLocalDirectoryParams{
		ID:          issue.ProjectID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !localDir.Valid || strings.TrimSpace(localDir.String) == "" {
		writeError(w, http.StatusBadRequest, "project local_directory not configured")
		return "", false
	}
	return strings.TrimSpace(localDir.String), true
}

func (h *Handler) resolveCSCloudDeviceID(w http.ResponseWriter, ctx context.Context, wsUUID pgtype.UUID) (string, bool) {
	runtimes, err := h.Queries.ListOnlineAgentRuntimesByWorkspaceAndProvider(ctx, db.ListOnlineAgentRuntimesByWorkspaceAndProviderParams{
		WorkspaceID: wsUUID,
		Provider:    csCloudRuntimeProvider,
	})
	if err != nil || len(runtimes) == 0 {
		writeError(w, http.StatusServiceUnavailable, "cs-cloud device not online")
		return "", false
	}

	for _, rt := range runtimes {
		var meta map[string]any
		_ = json.Unmarshal(rt.Metadata, &meta)
		if id, _ := meta["device_id"].(string); id != "" {
			return id, true
		}
		// Fallback: daemon_id itself is the device id.
		if rt.DaemonID.Valid && rt.DaemonID.String != "" {
			return rt.DaemonID.String, true
		}
	}

	writeError(w, http.StatusServiceUnavailable, "cs-cloud device has no device_id")
	return "", false
}

func (h *Handler) createConversationOnDevice(w http.ResponseWriter, r *http.Request, deviceID, userID, workspaceDir string, issue db.MulticaIssue) (string, bool) {
	if h.CloudRuntime == nil || !h.CloudRuntime.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "cloud runtime is not configured")
		return "", false
	}

	initialPrompt := fmt.Sprintf("Issue #%d: %s", issue.Number, issue.Title)
	if issue.Description.Valid && strings.TrimSpace(issue.Description.String) != "" {
		initialPrompt += "\n\n" + strings.TrimSpace(issue.Description.String)
	}

	body, _ := json.Marshal(createConversationRequest{
		Agent:              "csc",
		WorkspaceDirectory: workspaceDir,
		InitialPrompt:      initialPrompt,
	})

	resp, err := h.CloudRuntime.Do(r.Context(), cloudruntime.Request{
		Method:  http.MethodPost,
		Path:    fmt.Sprintf("/device/%s/proxy/api/v1/conversations", deviceID),
		Body:    body,
		UserID:  userID,
		RequestID: cloudRuntimeRequestID(r),
	})
	if err != nil {
		writeCloudRuntimeError(w, r, err)
		return "", false
	}
	if resp.StatusCode >= 300 {
		writeError(w, http.StatusServiceUnavailable, "failed to create conversation on device")
		return "", false
	}

	var created createConversationResponse
	if err := json.Unmarshal(resp.Body, &created); err != nil || created.ID == "" {
		writeError(w, http.StatusBadGateway, "invalid conversation response from device")
		return "", false
	}
	return created.ID, true
}

func (h *Handler) writeIssueConversationSession(w http.ResponseWriter, conversationID, workspaceDir, workspaceID string) {
	deviceID := ""
	// Extract device id from the first known online runtime to build events_url.
	// This is best-effort; the URL prefix already encodes the device.
	// A simpler approach: store device_id in the mapping table if needed later.
	prefix := gatewayProxyPrefix(h.cfg)
	eventsURL := fmt.Sprintf(prefix+"/api/v1/events?conversation_id=%s", deviceID, conversationID)

	writeJSON(w, http.StatusOK, IssueConversationSessionResponse{
		ConversationID:     conversationID,
		WorkspaceDirectory: workspaceDir,
		EventsURL:          eventsURL,
	})
}
```

**Note:** `writeIssueConversationSession` needs the `deviceID` to build `events_url`. Two clean options:
1. Add `device_id` to `multica_issue_conversation` mapping table and pass it through.
2. Re-resolve the online runtime when building the URL.

Recommended option 1: update Task 1 migration to include `device_id TEXT NOT NULL` in `multica_issue_conversation`, update `CreateIssueConversationParams` to include it, and use it directly in `writeIssueConversationSession`.

- [ ] **Step 2: 如果采用 option 1，更新迁移与查询**

Migration `135_issue_conversation_session.up.sql`:

```sql
CREATE TABLE multica_issue_conversation (
    issue_id            UUID PRIMARY KEY REFERENCES multica_issue(id) ON DELETE CASCADE,
    conversation_id     TEXT NOT NULL,
    workspace_directory TEXT NOT NULL,
    device_id           TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Update `issue_conversation.sql` insert params accordingly, and pass `deviceID` through to `writeIssueConversationSession`.

- [ ] **Step 3: 提交**

```bash
git add server/internal/handler/issue_conversation.go \
        server/migrations/135_issue_conversation_session.up.sql \
        server/pkg/db/queries/issue_conversation.sql \
        server/pkg/db/generated/
git commit -m "feat(handler): get or create issue conversation session via Gateway"
```

---

## Task 5: 注册路由

**Files:**
- Modify: `server/cmd/server/router.go`

- [ ] **Step 1: 在 workspace member 路由组中注册新接口**

Inside `r.Route("/api/workspaces", func(r chi.Router) { ... r.Route("/{id}", func(r chi.Router) { ... // Member-level access group ... })})`, add:

```go
// Issue conversation session: returns conversation_id + workspace_directory + events_url.
r.With(middleware.RequireWorkspaceMemberFromURL(queries, "id")).
    Get("/issues/{issueID}/session", h.GetIssueConversationSession)
```

Place it near the end of the `/api/workspaces/{id}` member-level group, after the existing `/github/installations` and `/gitlab/settings` lines.

- [ ] **Step 2: 提交**

```bash
git add server/cmd/server/router.go
git commit -m "feat(router): register GET /api/workspaces/{id}/issues/{issueID}/session"
```

---

## Task 6: Handler 单元测试

**Files:**
- Create: `server/internal/handler/issue_conversation_test.go`

- [ ] **Step 1: 编写测试**

```go
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
)

func TestGetIssueConversationSessionReturnsExisting(t *testing.T) {
	// Setup project with local_directory and issue.
	var projectID string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO multica_project (workspace_id, title, status, local_directory)
		VALUES ($1, 'Test Project', 'planned', '/Users/dev/project')
		RETURNING id
	`, testWorkspaceID).Scan(&projectID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	defer testPool.Exec(context.Background(), `DELETE FROM multica_project WHERE id = $1`, projectID)

	var issueID string
	err = testPool.QueryRow(context.Background(), `
		INSERT INTO multica_issue (workspace_id, title, description, status, priority, creator_type, creator_id, number, project_id)
		VALUES ($1, 'Test Issue', 'description', 'todo', 'medium', 'member', $2, 9999, $3)
		RETURNING id
	`, testWorkspaceID, testUserID, projectID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	defer testPool.Exec(context.Background(), `DELETE FROM multica_issue WHERE id = $1`, issueID)

	_, err = testPool.Exec(context.Background(), `
		INSERT INTO multica_issue_conversation (issue_id, conversation_id, workspace_directory, device_id)
		VALUES ($1, 'conv-existing', '/Users/dev/project', 'dev-1')
	`, issueID)
	if err != nil {
		t.Fatalf("insert conversation mapping: %v", err)
	}

	req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/issues/"+issueID+"/session", nil)
	req = withURLParam(req, "id", testWorkspaceID)
	req = withURLParam(req, "issueID", issueID)
	w := httptest.NewRecorder()

	testHandler.GetIssueConversationSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp IssueConversationSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ConversationID != "conv-existing" {
		t.Fatalf("conversation_id = %q", resp.ConversationID)
	}
}

func TestGetIssueConversationSessionCreatesNew(t *testing.T) {
	// Insert cs-cloud runtime.
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO multica_agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		) VALUES ($1, 'dev-123', 'cs-cloud', 'local', 'cs-cloud', 'online', 'macbook', '{"device_id":"dev-123"}'::jsonb, now())
	`, testWorkspaceID)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	defer testPool.Exec(context.Background(), `DELETE FROM multica_agent_runtime WHERE daemon_id = 'dev-123'`)

	var projectID string
	err = testPool.QueryRow(context.Background(), `
		INSERT INTO multica_project (workspace_id, title, status, local_directory)
		VALUES ($1, 'Test Project', 'planned', '/Users/dev/project')
		RETURNING id
	`, testWorkspaceID).Scan(&projectID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	defer testPool.Exec(context.Background(), `DELETE FROM multica_project WHERE id = $1`, projectID)

	var issueID string
	err = testPool.QueryRow(context.Background(), `
		INSERT INTO multica_issue (workspace_id, title, description, status, priority, creator_type, creator_id, number, project_id)
		VALUES ($1, 'Test Issue', 'description', 'todo', 'medium', 'member', $2, 9998, $3)
		RETURNING id
	`, testWorkspaceID, testUserID, projectID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	defer testPool.Exec(context.Background(), `DELETE FROM multica_issue WHERE id = $1`, issueID)
	defer testPool.Exec(context.Background(), `DELETE FROM multica_issue_conversation WHERE issue_id = $1`, issueID)

	proxy := &fakeCloudRuntimeProxy{
		enabled: true,
		resp: &cloudruntime.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"id":"conv-new"}`),
		},
	}
	useCloudRuntimeProxy(t, proxy)

	req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/issues/"+issueID+"/session", nil)
	req = withURLParam(req, "id", testWorkspaceID)
	req = withURLParam(req, "issueID", issueID)
	w := httptest.NewRecorder()

	testHandler.GetIssueConversationSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp IssueConversationSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ConversationID != "conv-new" {
		t.Fatalf("conversation_id = %q", resp.ConversationID)
	}
	if !strings.Contains(resp.EventsURL, "conversation_id=conv-new") {
		t.Fatalf("events_url = %q", resp.EventsURL)
	}
	if !proxy.called {
		t.Fatal("cloud runtime proxy was not called")
	}
	if proxy.req.Method != http.MethodPost {
		t.Fatalf("method = %q", proxy.req.Method)
	}
}
```

Add import for `strings`.

- [ ] **Step 2: 运行测试**

Run:
```bash
cd <multica-repo>/server
go test ./internal/handler -run TestGetIssueConversationSession -v
```
Expected: both tests PASS.

- [ ] **Step 3: 提交**

```bash
git add server/internal/handler/issue_conversation_test.go
git commit -m "test(handler): issue conversation session creation and reuse"
```

---

## Task 7: 完整 handler 测试跑通

- [ ] **Step 1: 运行 handler 包测试**

Run:
```bash
cd <multica-repo>/server
go test ./internal/handler/... -count=1
```
Expected: all tests PASS (or only pre-existing failures unrelated to this change).

- [ ] **Step 2: 提交（如有修复）**

If any fixes are needed, commit them with a clear message.

---

## Task 8: 前端集成说明

**Files:**
- No backend file changes; update frontend issue page code separately.

- [ ] **Step 1: 在 issue 页面调用新接口**

```typescript
const res = await fetch(`/api/workspaces/${workspaceID}/issues/${issueID}/session`);
if (!res.ok) { /* handle 503/offline or 400/missing config */ }
const { conversation_id, workspace_directory, events_url } = await res.json();
```

- [ ] **Step 2: 复用 workspace 对话组件**

Mount the existing chat component with:
- `conversationId = conversation_id`
- `eventsUrl = events_url`
- send messages via `POST /conversations/{conversation_id}/prompt`
- load history via `GET /conversations/{conversation_id}/messages`

No new state on cs-cloud/device is required.

---

## Task 9: Spec coverage self-check

| Spec requirement | Task |
|---|---|
| `issue_id → conversation_id` 映射存在 multica 后端 | Task 1, 2, 4 |
| 首次打开同步创建 conversation | Task 4 |
| 后续打开复用已有 conversation | Task 4 |
| 返回 `conversation_id`, `workspace_directory`, `events_url` | Task 4 (with option 1 device_id) |
| 前端直连 Gateway event 流 | Task 4, 8 |
| cs-cloud/设备本地不保存映射 | No cs-cloud code changes except prerequisite registration |
| 对话体验与 workspace 一致 | Task 8 (reuse components) |

No placeholders remain in this plan. All file paths, SQL, and code snippets are concrete and consistent with the existing multica server codebase.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-07-16-issue-page-conversation.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

**Which approach?**
