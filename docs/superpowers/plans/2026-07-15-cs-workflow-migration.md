# cs-workflow 能力迁移到 cs-cloud 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **状态说明：** 本文档已按当前实际实现更新。Phase 0–8 描述的代码与 `feat/cs-workflow-migration` 分支上的文件一致；已知的未完成项在 Phase 8 和 Self-Review Checklist 中标注。

**Goal:** 在 cs-cloud 中以代码隔离的方式新增 workflow 子系统，使其能够接收 CoStrict 云端通过 Gateway 下发的任务、维护 multica 工作区模型、调度本地 Agent CLI、直接回写任务状态到 multica 后端，并提供 `cs-cloud workflow *` CLI 子命令。

**Architecture:** 新增 `internal/workflow` 共享层与 `internal/workflowrunner` driver，扩展 `internal/runtime` 支持常驻型 persistent driver，在 `internal/localserver` 新增 `/api/v1/workflow/*` 路由作为 Gateway 入口，在 `internal/cli` 新增 workflow 子命令树。所有 workflow 状态写入 `~/.costrict/cs-cloud/workflow/`，不污染 cs-cloud 核心代码。

**Tech Stack:** Go 1.25+, 标准库 net/http, cs-cloud 现有 config/model/logger/platform/runtime/agent 模块。

---

## 文件结构总览

### 新增文件

| 文件 | 职责 |
|------|------|
| `internal/workflow/config.go` | WorkflowConfig 结构与默认值 |
| `internal/workflow/models.go` | workspace、issue、project、task、daemon 等 DTO |
| `internal/workflow/cache.go` | 本地 JSON 缓存读写（当前仅 workspaces） |
| `internal/workflow/protocol.go` | multica 后端 API 路径常量 |
| `internal/workflowrunner/driver.go` | workflow driver 实现：任务并发、注册、启停、abort tombstone |
| `internal/workflowrunner/types.go` | Config 别名与 driverState |
| `internal/workflowrunner/runtime.go` | driver 生命周期与后台 goroutine（sync / GC stub / heartbeat / maintain） |
| `internal/workflowrunner/client.go` | multica 后端 REST 客户端（无自动刷新、无 usage/session、无 claim） |
| `internal/workflowrunner/workspace.go` | 工作区/仓库缓存/worktree 创建（含 `.cs-workflow-ref` 标记） |
| `internal/workflowrunner/task.go` | 任务执行器（单次输出上报，无实时流，无 COSTRICT_TOKEN 注入） |
| `internal/localserver/workflow_handler.go` | `/api/v1/workflow/*` 路由 handler（health / run / abort） |
| `internal/cli/workflow.go` | `cs-cloud workflow` 子命令入口（issue/project/task stub） |
| `internal/cli/workflow_workspace.go` | `cs-cloud workflow workspace list/sync` |

### 修改文件

| 文件 | 修改内容 |
|------|----------|
| `internal/config/config.go` | 添加 `Workflow workflow.Config` 字段 |
| `internal/config/load.go` | 从环境变量/配置文件加载 workflow 配置；`MulticaBaseURL` 默认空，支持从 `COSTRICT_BASE_URL` 派生（`$BASE/workflow-backend`），显式 env/file 配置优先；最终为空则 `Load()` 报错 |
| `internal/localserver/server.go` | 注册 workflow 路由（health、run、abort）；直接持有 workflow driver；`Start` 中显式启动 workflow，失败阻断启动；`Shutdown` 中显式停止 |
| `internal/localserver/workflow_handler.go` | handler 直接调用 `s.workflow.RunTaskAsync` / `s.workflow.AbortTask` |
| `internal/cli/root.go` | dispatch 增加 `case "workflow"` |
| `internal/cli/serve.go` / `internal/cli/daemon.go` | 使用 `localserver.WithWorkflow(...)` 注入 driver |
| `internal/app/app.go` | 提供 `NewWorkflowDriver` 工厂方法，注入 deviceID、multica base URL、token provider |

---

## Phase 0: 基础设施

### Task 0.1: 新增 WorkflowConfig

**Files:**
- Create: `internal/workflow/config.go`
- Modify: `internal/config/config.go`
- Test: `internal/workflow/config_test.go`

- [x] **Step 1: Write the failing test**

```go
package workflow

import (
	"testing"
	"time"
)

func TestDefaultWorkflowConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WorkspacesRoot == "" {
		t.Fatal("WorkspacesRoot should not be empty")
	}
	if cfg.CacheDir == "" {
		t.Fatal("CacheDir should not be empty")
	}
	if cfg.SyncInterval == 0 {
		t.Fatal("SyncInterval should not be zero")
	}
	if cfg.GCInterval == 0 {
		t.Fatal("GCInterval should not be zero")
	}
	if cfg.HeartbeatInterval == 0 {
		t.Fatal("HeartbeatInterval should not be zero")
	}
	if cfg.AgentTimeout == 0 {
		t.Fatal("AgentTimeout should not be zero")
	}
	if cfg.MaxConcurrentTasks == 0 {
		t.Fatal("MaxConcurrentTasks should not be zero")
	}
	if len(cfg.AllowedAgents) == 0 {
		t.Fatal("AllowedAgents should not be empty")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/workflow -run TestDefaultWorkflowConfig -v`
Expected: FAIL with "undefined: DefaultConfig" or similar.

- [x] **Step 3: Write minimal implementation**

```go
// internal/workflow/config.go
package workflow

import (
	"path/filepath"
	"time"

	"cs-cloud/internal/platform"
)

type Config struct {
	MulticaBaseURL     string        `json:"multica_base_url"`
	WorkspacesRoot     string        `json:"workspaces_root"`
	CacheDir           string        `json:"cache_dir"`
	SyncInterval       time.Duration `json:"sync_interval"`
	GCInterval         time.Duration `json:"gc_interval"`
	HeartbeatInterval  time.Duration `json:"heartbeat_interval"`
	AgentTimeout       time.Duration `json:"agent_timeout"`
	MaxConcurrentTasks int           `json:"max_concurrent_tasks"`
	AllowedAgents      []string      `json:"allowed_agents"`
}

func DefaultConfig() Config {
	appDir := platform.AppDir()
	return Config{
		MulticaBaseURL:     "",
		WorkspacesRoot:     filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:           filepath.Join(appDir, "workflow", "cache"),
		SyncInterval:       5 * time.Minute,
		GCInterval:         24 * time.Hour,
		HeartbeatInterval:  15 * time.Second,
		AgentTimeout:       30 * time.Minute,
		MaxConcurrentTasks: 20,
		AllowedAgents: []string{
			"claude",
			"codex",
			"csc",
			"cs",
			"acp",
		},
	}
}
```

> 注意：`MulticaBaseURL` 不再有默认值；生产环境必须通过 `COSTRICT_BASE_URL` 派生或显式设置 `CS_CLOUD_WORKFLOW_MULTICA_BASE_URL`。

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/workflow -run TestDefaultWorkflowConfig -v`
Expected: PASS

- [x] **Step 5: Integrate into Config struct**

Modify `internal/config/config.go`:

```go
import "cs-cloud/internal/workflow"

type Config struct {
	// ... existing fields ...
	Workflow workflow.Config `json:"workflow"`
}
```

- [x] **Step 6: Commit**

```bash
git add internal/workflow/config.go internal/workflow/config_test.go internal/config/config.go
git commit -m "feat(workflow): add WorkflowConfig with defaults

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.2: 加载 WorkflowConfig

**Files:**
- Modify: `internal/config/load.go`
- Test: `internal/config/load_test.go`

- [x] **Step 1: Write the failing test**

```go
package config

import (
	"testing"
	"time"

	"cs-cloud/internal/platform"
)

func TestLoadWorkflowConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() { platform.SetDataDir("") })

	// Provide a base URL so the required multica URL derivation succeeds.
	t.Setenv("COSTRICT_BASE_URL", "https://example.costrict.local")
	t.Setenv("CS_CLOUD_WORKFLOW_WORKSPACES_ROOT", "/tmp/wf-workspaces")
	t.Setenv("CS_CLOUD_WORKFLOW_SYNC_INTERVAL", "10m")
	t.Setenv("CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS", "42")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Workflow.WorkspacesRoot != "/tmp/wf-workspaces" {
		t.Fatalf("WorkspacesRoot = %q", cfg.Workflow.WorkspacesRoot)
	}
	if cfg.Workflow.SyncInterval != 10*time.Minute {
		t.Fatalf("SyncInterval = %v", cfg.Workflow.SyncInterval)
	}
	if cfg.Workflow.MaxConcurrentTasks != 42 {
		t.Fatalf("MaxConcurrentTasks = %d", cfg.Workflow.MaxConcurrentTasks)
	}
	if cfg.Workflow.MulticaBaseURL != "https://example.costrict.local/workflow-backend" {
		t.Fatalf("MulticaBaseURL = %q", cfg.Workflow.MulticaBaseURL)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run TestLoadWorkflowConfigFromEnv -v`
Expected: FAIL, workflow fields not loaded or multica URL not derived.

- [x] **Step 3: Implement config loading**

Modify `internal/config/load.go`:

```go
import (
	// ... existing imports ...
	"cs-cloud/internal/workflow"
	"strconv"
	"strings"
	"time"
)

func Load() (*Config, error) {
	cfg := &Config{
		// ... existing fields ...
		Workflow: workflow.DefaultConfig(),
	}

	// Workflow config from env
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_MULTICA_BASE_URL"); v != "" {
		cfg.Workflow.MulticaBaseURL = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_WORKSPACES_ROOT"); v != "" {
		cfg.Workflow.WorkspacesRoot = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_CACHE_DIR"); v != "" {
		cfg.Workflow.CacheDir = v
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.SyncInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_GC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.GCInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_HEARTBEAT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.HeartbeatInterval = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_AGENT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Workflow.AgentTimeout = d
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workflow.MaxConcurrentTasks = n
		}
	}
	if v := platform.Getenv("CS_CLOUD_WORKFLOW_ALLOWED_AGENTS"); v != "" {
		cfg.Workflow.AllowedAgents = strings.Split(v, ",")
	}

	// ... rest of Load() (file config merging, etc.) ...
	// When merging file config:
	cfg.Workflow = mergeWorkflowConfig(cfg.Workflow, fileCfg.Workflow)

	// Derive multica URL from CoStrict base URL when not explicitly configured.
	if cfg.Workflow.MulticaBaseURL == "" && cfg.BaseURL != "" {
		cfg.Workflow.MulticaBaseURL = strings.TrimRight(cfg.BaseURL, "/") + "/workflow-backend"
	}

	if cfg.Workflow.MulticaBaseURL == "" {
		return nil, fmt.Errorf("workflow multica base URL is required; set COSTRICT_BASE_URL or CS_CLOUD_WORKFLOW_MULTICA_BASE_URL")
	}

	return cfg, nil
}

func mergeWorkflowConfig(current, file workflow.Config) workflow.Config {
	defaults := workflow.DefaultConfig()
	if file.MulticaBaseURL != "" && current.MulticaBaseURL == "" {
		current.MulticaBaseURL = file.MulticaBaseURL
	}
	if file.WorkspacesRoot != "" && current.WorkspacesRoot == defaults.WorkspacesRoot {
		current.WorkspacesRoot = file.WorkspacesRoot
	}
	if file.CacheDir != "" && current.CacheDir == defaults.CacheDir {
		current.CacheDir = file.CacheDir
	}
	if file.SyncInterval != 0 && current.SyncInterval == defaults.SyncInterval {
		current.SyncInterval = file.SyncInterval
	}
	if file.GCInterval != 0 && current.GCInterval == defaults.GCInterval {
		current.GCInterval = file.GCInterval
	}
	if file.HeartbeatInterval != 0 && current.HeartbeatInterval == defaults.HeartbeatInterval {
		current.HeartbeatInterval = file.HeartbeatInterval
	}
	if file.AgentTimeout != 0 && current.AgentTimeout == defaults.AgentTimeout {
		current.AgentTimeout = file.AgentTimeout
	}
	if file.MaxConcurrentTasks != 0 && current.MaxConcurrentTasks == defaults.MaxConcurrentTasks {
		current.MaxConcurrentTasks = file.MaxConcurrentTasks
	}
	if len(file.AllowedAgents) > 0 && len(current.AllowedAgents) == len(defaults.AllowedAgents) {
		current.AllowedAgents = file.AllowedAgents
	}
	return current
}
```

- [x] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/config -run TestLoadWorkflowConfigFromEnv -v
go test ./internal/config -run TestLoad_WorkflowMulticaBaseURL -v
```
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/config/load.go internal/config/load_test.go
git commit -m "feat(config): load WorkflowConfig from env and derive multica URL

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.3: 新增 workflow 共享模型

**Files:**
- Create: `internal/workflow/models.go`
- Test: `internal/workflow/models_test.go`

- [x] **Step 1–4:** 与原有计划一致，模型实现如下（含当前实际字段）：

```go
// internal/workflow/models.go
package workflow

import "time"

type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Issue struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Project struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	RepoURL     string `json:"repo_url,omitempty"`
}

type TaskStatus int

const (
	TaskStatusPending TaskStatus = iota
	TaskStatusRunning
	TaskStatusComplete
	TaskStatusFailed
	TaskStatusAborted
)

func (s TaskStatus) String() string { /* ... */ }

type Task struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	IssueID     string     `json:"issue_id,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	Agent       string     `json:"agent"`
	Prompt      string     `json:"prompt"`
	Status      TaskStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
}

type TaskRunPayload struct {
	TaskID      string            `json:"task_id"`
	WorkspaceID string            `json:"workspace_id"`
	IssueID     string            `json:"issue_id,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	Agent       string            `json:"agent"`
	Prompt      string            `json:"prompt"`
	Env         map[string]string `json:"env,omitempty"`
	Kind        string            `json:"kind,omitempty"`
}

type TaskMessage struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

type DaemonRuntime struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

type DaemonRegisterRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	DaemonID    string          `json:"daemon_id"`
	DeviceName  string          `json:"device_name,omitempty"`
	CLIVersion  string          `json:"cli_version,omitempty"`
	Runtimes    []DaemonRuntime `json:"runtimes"`
}

type DaemonRuntimeResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
}

type DaemonRegisterResponse struct {
	Runtimes     []DaemonRuntimeResponse `json:"runtimes"`
	Repos        []any                   `json:"repos,omitempty"`
	ReposVersion string                  `json:"repos_version,omitempty"`
	Settings     map[string]any          `json:"settings,omitempty"`
}
```

- [x] **Step 5: Commit**

```bash
git add internal/workflow/models.go internal/workflow/models_test.go
git commit -m "feat(workflow): add shared workflow models

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.4: 新增 multica 协议常量

**Files:**
- Create: `internal/workflow/protocol.go`

- [x] **Step 1: Write implementation**

```go
// internal/workflow/protocol.go
package workflow

const (
	MulticaDaemonRegisterEndpoint   = "/api/daemon/register"
	MulticaDaemonHeartbeatEndpoint  = "/api/daemon/heartbeat"
	MulticaDaemonDeregisterEndpoint = "/api/daemon/deregister"

	MulticaTaskClaimEndpoint    = "/api/daemon/runtimes/%s/tasks/claim"
	MulticaTaskStartEndpoint    = "/api/daemon/tasks/%s/start"
	MulticaTaskCompleteEndpoint = "/api/daemon/tasks/%s/complete"
	MulticaTaskFailEndpoint     = "/api/daemon/tasks/%s/fail"
	MulticaTaskUsageEndpoint    = "/api/daemon/tasks/%s/usage"
	MulticaTaskMessagesEndpoint = "/api/daemon/tasks/%s/messages"
	MulticaTaskSessionEndpoint  = "/api/daemon/tasks/%s/session"

	MulticaWorkspacesEndpoint = "/api/workspaces"
	MulticaIssuesEndpoint     = "/api/workspaces/%s/issues"
	MulticaProjectsEndpoint   = "/api/workspaces/%s/projects"

	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
)
```

> 当前 client 仅实现了 register/heartbeat/deregister、start/complete/fail/messages、workspaces/projects；`claim`、`usage`、`session` 有常量定义但尚未调用。

- [x] **Step 2: Commit**

```bash
git add internal/workflow/protocol.go
git commit -m "feat(workflow): add multica protocol constants

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.5: 新增本地 JSON 缓存

**Files:**
- Create: `internal/workflow/cache.go`
- Test: `internal/workflow/cache_test.go`

- [x] 实现与原有计划一致：`NewCache(dir)`、`WriteWorkspaces`、`ReadWorkspaces`。

- [x] **Commit**

```bash
git add internal/workflow/cache.go internal/workflow/cache_test.go
git commit -m "feat(workflow): add JSON cache for workspace metadata

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 1: 决策：不引入 PersistentDriver 抽象

经过 review，决定不在本 PR 引入 `PersistentDriver` 接口及 `AgentManager` 的 persistent driver 注册机制，原因：

1. `PersistentDriver` 接口只包含生命周期方法，不包含 `RunTask` / `AbortTask`，localserver 仍需类型断言，没有解耦。
2. `AgentManager` 原本只管理 per-conversation AI agent，混入 persistent driver 会污染语义。
3. cs-cloud 中已有 `filewatcher`、`gitwatcher` 等常驻组件采用“`Server` 直接持有 + 显式启停”模式，workflow 与其保持一致，避免同一文件里两套机制并存。
4. 当前只有 workflow 一个常驻子系统，等未来常驻组件达到 3+ 个，再统一抽象 `Component` / `ComponentManager`。

因此：

- **不创建** `internal/runtime/persistent_driver.go`。
- **不修改** `internal/runtime/manager.go` 增加 persistent driver 相关方法。
- workflow driver 由 `internal/localserver/server.go` 直接持有，见 Phase 2.2。

---

## Phase 2: workflow driver 骨架

### Task 2.1: 创建 workflow driver 类型与编译期检查

**Files:**
- Create: `internal/agent/workflow/driver.go`
- Create: `internal/agent/workflow/types.go`
- Test: `internal/agent/workflow/driver_test.go`

- [x] **实现**

```go
// internal/agent/workflow/types.go
package workflow

import "cs-cloud/internal/workflow"

// Config aliases the workflow package config so callers can use
// workflow.Config directly.
type Config = workflow.Config

type driverState int

const (
	driverStateIdle driverState = iota
	driverStateRunning
	driverStateError
)
```

```go
// internal/agent/workflow/driver.go
package workflow

import (
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

const providerCSCloud = "cs-cloud"

type Driver struct {
	cfg              workflow.Config
	deps             *Dependencies
	workspaceManager *WorkspaceManager
	client           *Client
	runtime          *runtimeLoop
	runner           *TaskRunner
	state            driverState
	sem              chan struct{}
	running          map[string]*taskRecord
	abortedIDs       map[string]time.Time
	registrations    map[string]string
	mu               sync.Mutex
}

type taskRecord struct {
	cancel  context.CancelFunc
	aborted bool
}

// NewDriver creates a new workflow driver.
func NewDriver(cfg workflow.Config, deps *Dependencies) *Driver {
	return &Driver{cfg: cfg, deps: deps}
}

// Name returns the driver name.
func (d *Driver) Name() string { return "workflow" }
```

- [x] **Commit**

```bash
git add internal/agent/workflow/driver.go internal/agent/workflow/types.go internal/agent/workflow/driver_test.go
git commit -m "feat(workflow): scaffold workflow driver

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2.2: 注册 workflow driver 到 cs-cloud 生命周期

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/localserver/server.go`

- [x] **Step 1: Add workflow driver construction in app**

Modify `internal/app/app.go`:

```go
import (
	// ... existing imports ...
	workflowagent "cs-cloud/internal/agent/workflow"
)

func (a *App) NewWorkflowDriver() *workflowagent.Driver {
	return workflowagent.NewDriver(a.cfg.Workflow, &workflowagent.Dependencies{
		MulticaBaseURL: a.cfg.Workflow.MulticaBaseURL,
		TokenProvider:  a.Credentials,
		DeviceID: func() (string, error) {
			dev, err := a.Device()
			if err != nil {
				return "", err
			}
			return dev.DeviceID, nil
		},
	})
}
```

- [x] **Step 2: Server 直接持有 workflow driver**

`internal/localserver/server.go`：

```go
import workflowagent "cs-cloud/internal/agent/workflow"

type Server struct {
    // ... existing fields ...
    workflow *workflowagent.Driver
}

func WithWorkflow(d *workflowagent.Driver) Option {
    return func(s *Server) {
        s.workflow = d
    }
}
```

- [x] **Step 3: Start/Stop workflow driver 在 Server 生命周期中显式调用**

`Start()` 中：

```go
if s.workflow != nil {
    if err := s.workflow.Start(); err != nil {
        return fmt.Errorf("start workflow driver: %w", err)
    }
}
```

`Shutdown()` 中：

```go
if s.workflow != nil {
    if err := s.workflow.Stop(); err != nil {
        logger.Error("Failed to stop workflow driver: %v", err)
    }
}
s.manager.KillAll()
```

- [x] **Step 4: Wire in serve/start flows**

找到 `localserver.New(...)` 的调用处（`internal/cli/serve.go` 和 `internal/cli/daemon.go`），注入：

```go
workflowDriver := a.NewWorkflowDriver()
server := localserver.New(
    localserver.WithConfig(cfg),
    localserver.WithWorkflow(workflowDriver),
    // ... other options ...
)
```

- [x] **Step 5: Verify build**

Run: `go build ./cmd/cs-cloud`
Expected: build succeeds.

- [x] **Step 6: Commit**

```bash
git add internal/app/app.go internal/localserver/server.go internal/localserver/workflow_handler.go internal/cli/serve.go internal/cli/daemon.go
git commit -m "feat(workflow): wire workflow driver directly into Server lifecycle

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 3: workspace/repo 管理

### Task 3.1: multica REST 客户端

**Files:**
- Create: `internal/agent/workflow/client.go`
- Test: `internal/agent/workflow/client_test.go`

- [x] **实现**

```go
// internal/agent/workflow/client.go
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

type Client struct {
	baseURL       string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string { /* ... */ }

var ErrRuntimeGone = errors.New("runtime gone")

func NewClient(baseURL string, tp func() (*provider.Credentials, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tp,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	cred, err := c.tokenProvider()
	if err != nil {
		return err
	}
	if cred == nil || cred.AccessToken == "" {
		return fmt.Errorf("no access token")
	}

	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workflow.HeaderClientPlatform, "cs-cloud")
	req.Header.Set(workflow.HeaderClientVersion, "dev")
	req.Header.Set(workflow.HeaderClientOS, runtime.GOOS)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return &StatusError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(b)}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) GetWorkspaces(ctx context.Context) ([]workflow.Workspace, error) {
	var out []workflow.Workspace
	err := c.request(ctx, http.MethodGet, workflow.MulticaWorkspacesEndpoint, nil, &out)
	return out, err
}

func (c *Client) GetProjects(ctx context.Context, workspaceID string) ([]workflow.Project, error) {
	var out []workflow.Project
	path := fmt.Sprintf(workflow.MulticaProjectsEndpoint, workspaceID)
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) StartTask(ctx context.Context, taskID string) error {
	path := fmt.Sprintf(workflow.MulticaTaskStartEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, nil, nil)
}

func (c *Client) CompleteTask(ctx context.Context, taskID string, output string) error {
	path := fmt.Sprintf(workflow.MulticaTaskCompleteEndpoint, taskID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"output": output}, nil)
}

func (c *Client) FailTask(ctx context.Context, taskID string, reason string, failureReason string) error {
	path := fmt.Sprintf(workflow.MulticaTaskFailEndpoint, taskID)
	body := map[string]any{"error": reason}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) PostTaskMessages(ctx context.Context, taskID string, output string) error {
	path := fmt.Sprintf(workflow.MulticaTaskMessagesEndpoint, taskID)
	msgs := []workflow.TaskMessage{{Seq: 1, Type: "text", Content: output}}
	return c.request(ctx, http.MethodPost, path, map[string]any{"messages": msgs}, nil)
}

func (c *Client) RegisterDaemon(ctx context.Context, req workflow.DaemonRegisterRequest) ([]workflow.DaemonRuntimeResponse, error) {
	var out workflow.DaemonRegisterResponse
	err := c.request(ctx, http.MethodPost, workflow.MulticaDaemonRegisterEndpoint, req, &out)
	return out.Runtimes, err
}

func (c *Client) Heartbeat(ctx context.Context, runtimeID string) error {
	err := c.request(ctx, http.MethodPost, workflow.MulticaDaemonHeartbeatEndpoint, map[string]any{"runtime_id": runtimeID}, nil)
	if err != nil {
		var stErr *StatusError
		if errors.As(err, &stErr) && stErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrRuntimeGone, err)
		}
	}
	return err
}

func (c *Client) DeregisterDaemon(ctx context.Context, runtimeIDs []string) error {
	return c.request(ctx, http.MethodPost, workflow.MulticaDaemonDeregisterEndpoint, map[string]any{"runtime_ids": runtimeIDs}, nil)
}
```

> 当前 client 使用静态 token provider，不自动刷新 CoStrict access token。

- [x] **Commit**

```bash
git add internal/agent/workflow/client.go internal/agent/workflow/client_test.go
git commit -m "feat(workflow): add multica REST client

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3.2 & 3.3: Workspace Manager / Repo Cache / Worktree

**Files:**
- Create: `internal/agent/workflow/workspace.go`
- Test: `internal/agent/workflow/workspace_test.go`

- [x] **实现要点**

```go
type WorkspaceManager struct{ root string }

func NewWorkspaceManager(root string) *WorkspaceManager
func (wm *WorkspaceManager) EnsureRoot() error
func (wm *WorkspaceManager) WorkspaceDir(workspaceID string) string
func (wm *WorkspaceManager) RepoCacheDir(workspaceID string) string
func (wm *WorkspaceManager) TaskWorktreeDir(workspaceID, taskID string) string
func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL string) (string, error)
func (wm *WorkspaceManager) CreateWorktree(workspaceID, taskID, repoURL, ref string) (string, error)
```

关键行为：
- 使用 `--mirror` 缓存仓库；首次 clone，后续 `remote update`。
- `CreateWorktree` 在 `repoURL` 为空时仅创建任务目录（不创建 git worktree）。
- 通过 `.cs-workflow-ref` 文件记录 worktree 对应的 ref；若 ref 变化则移除旧 worktree 重新创建。
- git 命令默认 5 分钟超时。

- [x] **Commit**

```bash
git add internal/agent/workflow/workspace.go internal/agent/workflow/workspace_test.go
git commit -m "feat(workflow): implement repo cache and worktree helpers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3.4: 元数据同步、GC stub、心跳/注册循环

**Files:**
- Create: `internal/agent/workflow/runtime.go`
- Test: `internal/agent/workflow/runtime_test.go`

- [x] **实现**

```go
type runtimeLoop struct {
	cfg          workflow.Config
	client       *Client
	cache        *workflow.Cache
	syncFunc     func() error
	gcFunc       func() error
	maintainFunc func() error
	// ... mutex / context / wg ...
}

func (r *runtimeLoop) Start() error {
	// 启动 3 个后台 loop：sync、GC、maintain（间隔分别为 SyncInterval、GCInterval、HeartbeatInterval）
}

func (r *runtimeLoop) doSync() error {
	// 拉取 workspaces 写入 cache
}

func (r *runtimeLoop) doGC() error {
	// 当前为 stub
	return nil
}

func (r *runtimeLoop) doMaintain() error {
	// 调用 driver.maintainRegistrations（register / heartbeat / re-register）
}
```

- [x] **Commit**

```bash
git add internal/agent/workflow/runtime.go internal/agent/workflow/runtime_test.go
git commit -m "feat(workflow): add metadata sync, GC stub, and heartbeat loops

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 4: 任务执行与状态上报

### Task 4.1: 任务执行器

**Files:**
- Create: `internal/agent/workflow/task.go`
- Test: `internal/agent/workflow/task_test.go`

- [x] **实现**

```go
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	agentTimeout     time.Duration
	allowedAgents    []string
}

func NewTaskRunner(wm *WorkspaceManager, timeout time.Duration, allowedAgents []string) *TaskRunner

func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) ([]byte, error) {
	repoURL, _ := tr.resolveRepoURL(ctx, payload.WorkspaceID, payload.ProjectID)
	worktree, err := tr.workspaceManager.CreateWorktree(payload.WorkspaceID, payload.TaskID, repoURL, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("prepare worktree: %w", err)
	}
	if err := tr.validateAgent(payload.Agent); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, payload.Agent)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)
	cmd.Stdin = strings.NewReader(payload.Prompt)

	return cmd.CombinedOutput()
}

func (tr *TaskRunner) buildEnv(payload workflow.TaskRunPayload, worktree string) []string {
	env := os.Environ()
	for k, v := range payload.Env {
		env = setEnv(env, k, v)
	}
	env = setEnv(env, "MULTICA_WORKSPACE_ID", payload.WorkspaceID)
	env = setEnv(env, "MULTICA_TASK_ID", payload.TaskID)
	env = setEnv(env, "MULTICA_PROMPT", payload.Prompt)
	env = setEnv(env, "CS_CLOUD_WORKTREE", worktree)
	return env
}
```

> 当前未向 agent 注入 `COSTRICT_TOKEN`；`resolveRepoURL` 为 no-op（project→repo 映射未实现）。

- [x] **Commit**

```bash
git add internal/agent/workflow/task.go internal/agent/workflow/task_test.go
git commit -m "feat(workflow): add task runner

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4.2: 完善 multica 任务状态 API

**Files:**
- Modify: `internal/agent/workflow/client.go`
- Test: `internal/agent/workflow/client_test.go`

- [x] 已实现方法（均带 `context.Context`）：
  - `StartTask`
  - `CompleteTask(ctx, taskID, output string)` — 发送 `{"output": output}`
  - `FailTask(ctx, taskID, reason, failureReason string)` — 发送 `{"error": reason, "failure_reason": failureReason}`（`failureReason` 为空时省略）
  - `PostTaskMessages(ctx, taskID, output string)` — 发送 `{"messages": [{"seq":1,"type":"text","content":output}]}`

> 未实现：`PostTaskUsage`、`PostTaskSession`。

- [x] **Commit**

```bash
git add internal/agent/workflow/client.go internal/agent/workflow/client_test.go
git commit -m "feat(workflow): implement multica task status APIs

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4.3: Driver 启动时组装所有组件并支持异步任务

**Files:**
- Modify: `internal/agent/workflow/driver.go`
- Modify: `internal/agent/workflow/types.go`（若需要新增 Dependencies）
- Test: `internal/agent/workflow/driver_test.go`

- [x] **Dependencies**

```go
type Dependencies struct {
	MulticaBaseURL string
	TokenProvider  func() (*provider.Credentials, error)
	DeviceID       func() (string, error)
}
```

- [x] **Driver.Start / Stop / Health / RunTask / RunTaskAsync / AbortTask**

关键行为：
- `Start` 初始化 workspaceManager、client、runtimeLoop、runner、semaphore、running map、abortedIDs tombstones、registrations map；设置 `runtime.maintainFunc = d.maintainRegistrations`；并在 goroutine 中立即尝试一次注册。
- `Stop` 取消 runtime loop、调用 `DeregisterDaemon`、取消所有运行中任务。
- `RunTask` 同步执行（用于测试或内部调用）。
- `RunTaskAsync` 同步 `reserve` 后立刻返回，实际执行在 detached goroutine 中完成 —— localserver handler 使用此方法避免 gateway ~30s 超时。
- `reserve` 使用 semaphore 限流，拒绝重复任务和先到达的 abort tombstone，同时清理过期 tombstone。
- `AbortTask` 取消运行中任务；若任务尚未运行则写入 `abortedIDs` tombstone。
- `execute` 调用 `StartTask`、runner.Run、PostTaskMessages、CompleteTask/FailTask。
- `maintainRegistrations` 拉取 workspace 列表，对每个 workspace 注册/心跳/重新注册 `provider=cs-cloud` runtime。

- [x] **Commit**

```bash
git add internal/agent/workflow/driver.go internal/agent/workflow/types.go internal/agent/workflow/driver_test.go
git commit -m "feat(workflow): wire driver components and add async task execution

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 5: LocalServer 路由

### Task 5.1: workflow handler

**Files:**
- Create: `internal/localserver/workflow_handler.go`
- Test: `internal/localserver/workflow_handler_test.go`

- [x] **实现**

```go
func (s *Server) handleWorkflowHealth(w http.ResponseWriter, r *http.Request)
func (s *Server) handleWorkflowTaskRun(w http.ResponseWriter, r *http.Request)
func (s *Server) handleWorkflowTaskAbort(w http.ResponseWriter, r *http.Request)
```

- handler 直接通过 `s.workflow` 调用；`s.workflow == nil` 时返回 404。
- `handleWorkflowTaskRun` 解码 `workflow.TaskRunPayload`，校验 `task_id`，调用 `s.workflow.RunTaskAsync`。
  - 成功返回 HTTP 200 `{"status":"accepted"}`。
  - `reserve` 失败（任务已在运行/队列满/pre-aborted）返回 HTTP 409 `CONFLICT`。
- `handleWorkflowTaskAbort` 调用 `s.workflow.AbortTask`；未找到任务返回 HTTP 404。

- [x] **Commit**

```bash
git add internal/localserver/workflow_handler.go internal/localserver/workflow_handler_test.go
git commit -m "feat(localserver): add workflow health, run, and abort handlers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5.2: 注册 workflow 路由

**Files:**
- Modify: `internal/localserver/server.go`

- [x] 在 `New()` 中注册：

```go
api.HandleFunc("GET /workflow/health", s.handleWorkflowHealth)
api.HandleFunc("POST /workflow/tasks/{id}/run", s.handleWorkflowTaskRun)
api.HandleFunc("POST /workflow/tasks/{id}/abort", s.handleWorkflowTaskAbort)
```

- [x] **Commit**

```bash
git add internal/localserver/server.go
git commit -m "feat(localserver): register workflow routes

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 6: CLI 子命令

### Task 6.1: workflow 子命令入口

**Files:**
- Create: `internal/cli/workflow.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/workflow_test.go`

- [x] 实现与原有计划一致，但 `workflowIssueCmd`、`workflowProjectCmd`、`workflowTaskCmd` 当前为 stub，直接返回 `not implemented yet`。

- [x] **Commit**

```bash
git add internal/cli/workflow.go internal/cli/root.go internal/cli/workflow_test.go
git commit -m "feat(cli): add workflow subcommand entrypoint

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6.2: workspace 子命令

**Files:**
- Create: `internal/cli/workflow_workspace.go`
- Test: `internal/cli/workflow_workspace_test.go`

- [x] 实现 `workflow workspace list/sync`；`sync` 使用 `context.WithTimeout(..., cfg.Workflow.AgentTimeout)`。

- [x] **Commit**

```bash
git add internal/cli/workflow_workspace.go
git commit -m "feat(cli): add workflow workspace list/sync commands

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 7: 集成与测试

### Task 7.1: HTTP 集成测试

**Files:**
- Create: `internal/localserver/workflow_http_test.go`

- [x] 测试覆盖要点：
  - `GET /workflow/health` 在 driver 健康时返回 `{"status":"ok"}`。
  - `POST /workflow/tasks/{id}/run` 成功返回 `{"status":"accepted"}`（任务在后台执行）。
  - 重复调用同一 `task_id` 返回 HTTP 409。
  - `POST /workflow/tasks/{id}/abort` 可中止任务或返回 404。

> 由于 `RunTaskAsync` 立即返回，测试断言只验证同步响应；任务实际执行结果通过 client mock 或日志验证。

- [x] **Commit**

```bash
git add internal/localserver/workflow_http_test.go
git commit -m "test(localserver): add workflow endpoint integration tests

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7.2: 端到端手动验证

- [x] **Step 1: Build cs-cloud**

Run: `go build -o bin/cs-cloud ./cmd/cs-cloud`
Expected: binary created.

- [x] **Step 2: Run cs-cloud serve**

Run: `./bin/cs-cloud serve`
Expected: localserver starts, workflow driver initializes and registers with multica.

- [x] **Step 3: Test workflow health endpoint**

Run: `curl http://127.0.0.1:<port>/api/v1/workflow/health`
Expected: `{"status":"ok"}`（driver running）或 `UNAVAILABLE`（driver 未启动）。

- [x] **Step 4: Test workflow workspace sync**

Run: `./bin/cs-cloud workflow workspace sync`
Expected: outputs synced workspace count or authentication error if multica backend unavailable.

- [x] **Step 5: Simulate Gateway task push**

CoStrict Gateway 通过内网隧道转发到 localserver：

```bash
curl -X POST http://127.0.0.1:<port>/api/v1/workflow/tasks/task-1/run \
  -H "Content-Type: application/json" \
  -d '{"task_id":"task-1","workspace_id":"ws-1","agent":"echo","prompt":"hello"}'
```

Expected: `{"ok":true,"data":{"status":"accepted"}}`；agent 在后台运行，状态通过 multica REST 回写。

- [x] **Step 6: Document manual verification results**

验证结果记录在本计划 Phase 7.2 或单独 verification log 中。

---

## Phase 8: 最终交付

### Task 8.1: 运行全量测试与文档更新

- [x] **Step 1: Run all workflow tests**

Run:
```bash
go test ./internal/workflow/... ./internal/workflowrunner/... ./internal/localserver/... ./internal/cli/... ./internal/config/... ./internal/runtime/...
```
Expected: all PASS

> 注意：运行 config/cli 测试前需确保 `COSTRICT_BASE_URL` 已设置，或测试 helper 已提供默认值；当前 `internal/config/load_test.go` 的 `isolatedConfig` 已默认设置 `COSTRICT_BASE_URL`。

- [x] **Step 2: Run go vet and build**

```bash
go vet ./...
go build ./cmd/cs-cloud
```
Expected: no errors

- [x] **Step 3: Update ARCHITECTURE.md**

已在 `ARCHITECTURE.md` 的"扩展点"中更新说明：workflow 作为常驻子系统由 `Server` 直接持有并显式启停，未使用 `PersistentDriver` 抽象；列出 workflow 已接入及当前 3 个路由。

- [x] **Step 4: Commit**

```bash
git add -A
git commit -m "docs(architecture): document workflow driver extension point

Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task 8.2: 创建 PR

- [x] **Step 1: Push branch**

Run: `git push origin feat/cs-workflow-migration`

- [x] **Step 2: Open PR targeting main**

Use `gh pr create` with title and description referencing the design doc.

- [x] **Step 3: Stop and wait for user review**

Do not merge without explicit user approval per CLAUDE.md rules.

---

## 已知未实现 / 当前限制

| 区域 | 说明 |
|------|------|
| Token 刷新 | workflow client 使用静态 token provider，不自动刷新 CoStrict access token；token 过期会导致 multica 调用 401。 |
| 实时输出 | agent 执行结果在退出后一次性上报，无 SSE/实时流。 |
| usage/session API | `MulticaTaskUsageEndpoint`、`MulticaTaskSessionEndpoint` 有常量但 client 未调用。 |
| GC | `runtimeLoop.doGC` 为 stub，未清理过期 worktree/缓存。 |
| COSTRICT_TOKEN | `TaskRunner.buildEnv` 未向 agent 子进程注入 `COSTRICT_TOKEN`。 |
| CLI | `issue`、`project`、`task` 子命令为 stub。 |
| repo 解析 | `TaskRunner.resolveRepoURL` 为 no-op，尚未根据 project 查询 repo_url。 |
| claim 拉取 | 当前任务由 CoStrict Gateway 通过 server-side push 下发，未使用 `MulticaTaskClaimEndpoint` 轮询。 |

---

## Self-Review Checklist

- [x] Spec coverage: every design section (config, driver, routes, CLI, error handling, testing) has corresponding tasks.
- [x] Placeholder scan: remaining TODOs are documented in "已知未实现 / 当前限制" above.
- [x] Server ownership: workflow driver is a direct field of `Server`, started/stopped explicitly in `Server.Start/Shutdown`, with start failure blocking daemon startup.
- [x] Config derivation: `MulticaBaseURL` defaults empty, derives from `COSTRICT_BASE_URL`, explicit env/file wins, and `Load()` fails fast when unset.
- [x] Async execution: `RunTaskAsync` returns `accepted` synchronously and runs agent in detached goroutine.
- [x] Abort race: `abortedIDs` tombstone with TTL handles abort-before-run.
- [x] Runtime registration: `provider=cs-cloud` registered/heartbeat/deregistered per workspace.
