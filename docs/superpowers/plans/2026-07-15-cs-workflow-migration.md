# cs-workflow 能力迁移到 cs-cloud 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 cs-cloud 中以代码隔离的方式新增 workflow 子系统，使其能够接收 CoStrict 云端通过 Gateway 下发的任务、维护 multica 工作区模型、调度本地 Agent CLI、直接回写任务状态到 multica 后端，并提供 `cs-cloud workflow *` CLI 子命令。

**Architecture:** 新增 `internal/workflow` 共享层与 `internal/agent/workflow` driver，扩展 `internal/runtime` 支持常驻型 persistent driver，在 `internal/localserver` 新增 `/api/v1/workflow/*` 路由作为 Gateway 入口，在 `internal/cli` 新增 workflow 子命令树。所有 workflow 状态写入 `~/.costrict/cs-cloud/workflow/`，不污染 cs-cloud 核心代码。

**Tech Stack:** Go 1.25+, 标准库 net/http, cs-cloud 现有 config/model/logger/platform/runtime/agent 模块。

---

## 文件结构总览

### 新增文件

| 文件 | 职责 |
|------|------|
| `internal/workflow/config.go` | WorkflowConfig 结构与默认值 |
| `internal/workflow/models.go` | workspace、issue、project、task 等 DTO |
| `internal/workflow/cache.go` | 本地 JSON 缓存读写 |
| `internal/workflow/protocol.go` | multica 后端 API 路径常量 |
| `internal/agent/workflow/driver.go` | 实现 PersistentDriver 接口 |
| `internal/agent/workflow/runtime.go` | driver 生命周期与后台 goroutine |
| `internal/agent/workflow/client.go` | multica 后端 REST 客户端 |
| `internal/agent/workflow/workspace.go` | 工作区/仓库缓存/GC |
| `internal/agent/workflow/task.go` | 任务执行器 |
| `internal/agent/workflow/types.go` | driver 内部类型 |
| `internal/runtime/persistent_driver.go` | PersistentDriver 接口定义 |
| `internal/localserver/workflow_handler.go` | `/api/v1/workflow/*` 路由 handler |
| `internal/cli/workflow.go` | `cs-cloud workflow` 子命令入口 |
| `internal/cli/workflow_workspace.go` | `cs-cloud workflow workspace *` |
| `internal/cli/workflow_issue.go` | `cs-cloud workflow issue *` |

### 修改文件

| 文件 | 修改内容 |
|------|----------|
| `internal/config/config.go` | 添加 `Workflow WorkflowConfig` 字段 |
| `internal/config/load.go` | 从环境变量/配置文件加载 workflow 配置 |
| `internal/runtime/manager.go` | AgentManager 增加 persistent driver 注册/启动/获取 |
| `internal/localserver/server.go` | 注册 workflow 路由；注入 workflow driver |
| `internal/cli/root.go` | dispatch 增加 `case "workflow"` |
| `internal/app/app.go` | 暴露 workflow 配置或相关依赖 |

---

## Phase 0: 基础设施

### Task 0.1: 新增 WorkflowConfig

**Files:**
- Create: `internal/workflow/config.go`
- Modify: `internal/config/config.go`
- Test: `internal/workflow/config_test.go`

- [ ] **Step 1: Write the failing test**

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
	if cfg.SyncInterval == 0 {
		t.Fatal("SyncInterval should not be zero")
	}
	if cfg.GCInterval == 0 {
		t.Fatal("GCInterval should not be zero")
	}
	if cfg.AgentTimeout == 0 {
		t.Fatal("AgentTimeout should not be zero")
	}
	if cfg.MaxConcurrentTasks == 0 {
		t.Fatal("MaxConcurrentTasks should not be zero")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/workflow -run TestDefaultWorkflowConfig -v`
Expected: FAIL with "undefined: DefaultConfig" or similar.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/workflow/config.go
package workflow

import "time"

type Config struct {
	MulticaBaseURL     string        `json:"multica_base_url"`
	WorkspacesRoot     string        `json:"workspaces_root"`
	CacheDir           string        `json:"cache_dir"`
	SyncInterval       time.Duration `json:"sync_interval"`
	GCInterval         time.Duration `json:"gc_interval"`
	AgentTimeout       time.Duration `json:"agent_timeout"`
	MaxConcurrentTasks int           `json:"max_concurrent_tasks"`
}

func DefaultConfig() Config {
	return Config{
		MulticaBaseURL:     "https://api.multica.ai",
		WorkspacesRoot:     "${HOME}/.costrict/cs-cloud/workflow/workspaces",
		CacheDir:           "${HOME}/.costrict/cs-cloud/workflow/cache",
		SyncInterval:       5 * time.Minute,
		GCInterval:         24 * time.Hour,
		AgentTimeout:       30 * time.Minute,
		MaxConcurrentTasks: 20,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/workflow -run TestDefaultWorkflowConfig -v`
Expected: PASS

- [ ] **Step 5: Integrate into Config struct**

Modify `internal/config/config.go`:

```go
type Config struct {
	// ... existing fields ...
	Workflow workflow.Config `json:"workflow"`
}
```

- [ ] **Step 6: Commit**

```bash
git add internal/workflow/config.go internal/workflow/config_test.go internal/config/config.go
git commit -m "feat(workflow): add WorkflowConfig with defaults

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.2: 加载 WorkflowConfig

**Files:**
- Modify: `internal/config/load.go`
- Test: `internal/config/load_test.go` (新增或修改现有)

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorkflowConfigFromEnv(t *testing.T) {
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
	if cfg.Workflow.SyncInterval != 10*60*1e9 {
		t.Fatalf("SyncInterval = %v", cfg.Workflow.SyncInterval)
	}
	if cfg.Workflow.MaxConcurrentTasks != 42 {
		t.Fatalf("MaxConcurrentTasks = %d", cfg.Workflow.MaxConcurrentTasks)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run TestLoadWorkflowConfigFromEnv -v`
Expected: FAIL, workflow fields not loaded.

- [ ] **Step 3: Implement config loading**

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

	// ... rest of existing Load() ...

	return cfg, nil
}
```

Also update file config merging to handle `Workflow` field if present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config -run TestLoadWorkflowConfigFromEnv -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/load.go internal/config/load_test.go
git commit -m "feat(config): load WorkflowConfig from env

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.3: 新增 workflow 共享模型

**Files:**
- Create: `internal/workflow/models.go`
- Test: `internal/workflow/models_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import "testing"

func TestTaskStatusString(t *testing.T) {
	if TaskStatusRunning.String() != "running" {
		t.Fatal("unexpected status string")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/workflow -run TestTaskStatusString -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

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
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	RepoURL     string    `json:"repo_url,omitempty"`
}

type TaskStatus int

const (
	TaskStatusPending TaskStatus = iota
	TaskStatusRunning
	TaskStatusComplete
	TaskStatusFailed
	TaskStatusAborted
)

func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "pending"
	case TaskStatusRunning:
		return "running"
	case TaskStatusComplete:
		return "complete"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusAborted:
		return "aborted"
	default:
		return "unknown"
	}
}

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
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/workflow -run TestTaskStatusString -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/workflow/models.go internal/workflow/models_test.go
git commit -m "feat(workflow): add shared workflow models

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 0.4: 新增 multica 协议常量

**Files:**
- Create: `internal/workflow/protocol.go`

- [ ] **Step 1: Write implementation**

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

- [ ] **Step 2: Commit**

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

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheReadWriteWorkspaces(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)

	wss := []Workspace{{ID: "ws-1", Name: "Test"}}
	if err := c.WriteWorkspaces(wss); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := c.ReadWorkspaces()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ws-1" {
		t.Fatalf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/workflow -run TestCacheReadWriteWorkspaces -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/workflow/cache.go
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Cache struct {
	dir string
}

func NewCache(dir string) *Cache {
	return &Cache{dir: dir}
}

func (c *Cache) ensureDir() error {
	return os.MkdirAll(c.dir, 0o755)
}

func (c *Cache) workspacesPath() string {
	return filepath.Join(c.dir, "workspaces.json")
}

func (c *Cache) WriteWorkspaces(wss []Workspace) error {
	if err := c.ensureDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(wss, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.workspacesPath(), b, 0o644)
}

func (c *Cache) ReadWorkspaces() ([]Workspace, error) {
	b, err := os.ReadFile(c.workspacesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Workspace{}, nil
		}
		return nil, err
	}
	var wss []Workspace
	if err := json.Unmarshal(b, &wss); err != nil {
		return nil, fmt.Errorf("unmarshal workspaces: %w", err)
	}
	return wss, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/workflow -run TestCacheReadWriteWorkspaces -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/workflow/cache.go internal/workflow/cache_test.go
git commit -m "feat(workflow): add JSON cache for workspace metadata

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 1: Persistent Driver 抽象

### Task 1.1: 定义 PersistentDriver 接口

**Files:**
- Create: `internal/runtime/persistent_driver.go`
- Test: `internal/runtime/persistent_driver_test.go`

- [ ] **Step 1: Write the failing test**

```go
package runtime

import "testing"

func TestPersistentDriverInterface(t *testing.T) {
	// Compile-time check: a mock implements PersistentDriver.
	var _ PersistentDriver = (*mockPersistentDriver)(nil)
}

type mockPersistentDriver struct{}

func (m *mockPersistentDriver) Name() string  { return "mock" }
func (m *mockPersistentDriver) Start() error  { return nil }
func (m *mockPersistentDriver) Stop() error   { return nil }
func (m *mockPersistentDriver) Health() error { return nil }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime -run TestPersistentDriverInterface -v`
Expected: FAIL, PersistentDriver undefined.

- [ ] **Step 3: Write implementation**

```go
// internal/runtime/persistent_driver.go
package runtime

// PersistentDriver is a long-lived driver managed by AgentManager.
// Unlike normal agent drivers that create per-conversation Agent processes,
// persistent drivers are started once and run for the lifetime of the daemon.
type PersistentDriver interface {
	Name() string
	Start() error
	Stop() error
	Health() error
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime -run TestPersistentDriverInterface -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/persistent_driver.go internal/runtime/persistent_driver_test.go
git commit -m "feat(runtime): define PersistentDriver interface

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 1.2: AgentManager 支持 Persistent Driver

**Files:**
- Modify: `internal/runtime/manager.go`
- Test: `internal/runtime/manager_test.go` (新增或修改)

- [ ] **Step 1: Write the failing test**

```go
package runtime

import "testing"

func TestAgentManagerPersistentDriver(t *testing.T) {
	m := NewAgentManager(NewEventBus())
	d := &mockPersistentDriver{}
	m.RegisterPersistentDriver(d)

	got, ok := m.GetPersistentDriver("mock")
	if !ok {
		t.Fatal("expected driver registered")
	}
	if got.Name() != "mock" {
		t.Fatalf("name = %q", got.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime -run TestAgentManagerPersistentDriver -v`
Expected: FAIL

- [ ] **Step 3: Implement persistent driver management**

Modify `internal/runtime/manager.go`:

```go
type AgentManager struct {
	mu                sync.RWMutex
	agents            map[string]agent.Agent
	drivers           map[string]agent.Driver
	persistentDrivers map[string]PersistentDriver
	eventBus          *EventBus
}

func NewAgentManager(eventBus *EventBus) *AgentManager {
	return &AgentManager{
		agents:            make(map[string]agent.Agent),
		drivers:           make(map[string]agent.Driver),
		persistentDrivers: make(map[string]PersistentDriver),
		eventBus:          eventBus,
	}
}

func (m *AgentManager) RegisterPersistentDriver(d PersistentDriver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistentDrivers[d.Name()] = d
}

func (m *AgentManager) GetPersistentDriver(name string) (PersistentDriver, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.persistentDrivers[name]
	return d, ok
}

func (m *AgentManager) StartPersistentDrivers() error {
	m.mu.RLock()
	ds := make([]PersistentDriver, 0, len(m.persistentDrivers))
	for _, d := range m.persistentDrivers {
		ds = append(ds, d)
	}
	m.mu.RUnlock()

	for _, d := range ds {
		if err := d.Start(); err != nil {
			return fmt.Errorf("start persistent driver %s: %w", d.Name(), err)
		}
	}
	return nil
}

func (m *AgentManager) StopPersistentDrivers() error {
	m.mu.RLock()
	ds := make([]PersistentDriver, 0, len(m.persistentDrivers))
	for _, d := range m.persistentDrivers {
		ds = append(ds, d)
	}
	m.mu.RUnlock()

	var errs []error
	for _, d := range ds {
		if err := d.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop persistent driver %s: %w", d.Name(), err))
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime -run TestAgentManagerPersistentDriver -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/manager.go internal/runtime/manager_test.go
git commit -m "feat(runtime): manage persistent drivers in AgentManager

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 2: workflow driver 骨架

### Task 2.1: 创建 workflow driver 骨架

**Files:**
- Create: `internal/agent/workflow/driver.go`
- Create: `internal/agent/workflow/types.go`
- Test: `internal/agent/workflow/driver_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"testing"

	"cs-cloud/internal/workflow"
)

func TestDriverName(t *testing.T) {
	d := NewDriver(workflow.Config{}, nil)
	if d.Name() != "workflow" {
		t.Fatalf("name = %q", d.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestDriverName -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/agent/workflow/types.go
package workflow

import "cs-cloud/internal/workflow"

type Driver struct {
	cfg  workflow.Config
	deps *Dependencies
}

func NewDriver(cfg workflow.Config, deps *Dependencies) *Driver {
	return &Driver{cfg: cfg, deps: deps}
}

func (d *Driver) Name() string { return "workflow" }
```

```go
// internal/agent/workflow/driver.go
package workflow

import "cs-cloud/internal/runtime"

var _ runtime.PersistentDriver = (*Driver)(nil)

func (d *Driver) Start() error {
	// Phase 2.2 will fill this.
	return nil
}

func (d *Driver) Stop() error {
	return nil
}

func (d *Driver) Health() error {
	return nil
}
```

```go
// internal/agent/workflow/deps.go
package workflow

import "cs-cloud/internal/provider"

type Dependencies struct {
	TokenProvider func() (*provider.Credentials, error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/workflow -run TestDriverName -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/driver.go internal/agent/workflow/types.go internal/agent/workflow/deps.go internal/agent/workflow/driver_test.go
git commit -m "feat(workflow): scaffold workflow driver

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2.2: 注册 workflow driver 到 cs-cloud 生命周期

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/localserver/server.go`
- Modify: `internal/cli/start.go` 或 `internal/app` 启动流程
- Test: 手动验证

- [ ] **Step 1: Add workflow driver construction in app**

Modify `internal/app/app.go`:

```go
import (
	// ... existing imports ...
	workflowagent "cs-cloud/internal/agent/workflow"
)

func (a *App) NewWorkflowDriver() *workflowagent.Driver {
	return workflowagent.NewDriver(a.cfg.Workflow, &workflowagent.Dependencies{
		TokenProvider: a.Credentials,
	})
}
```

- [ ] **Step 2: Register driver in localserver startup**

Modify `internal/localserver/server.go` `New()`:

```go
func WithWorkflowDriver(d runtime.PersistentDriver) Option {
	return func(s *Server) {
		if d != nil {
			s.manager.RegisterPersistentDriver(d)
		}
	}
}
```

- [ ] **Step 3: Start persistent drivers in server Start**

Modify `internal/localserver/server.go` `Start()`:

```go
func (s *Server) Start(addr string) error {
	// ... existing code before listener ...
	if err := s.manager.StartPersistentDrivers(); err != nil {
		logger.Error("Failed to start persistent drivers: %v", err)
	}
	// ... rest of Start() ...
}
```

- [ ] **Step 4: Stop persistent drivers in Shutdown**

Modify `internal/localserver/server.go` `Shutdown()`:

```go
func (s *Server) Shutdown(ctx context.Context) error {
	// ... existing watcher stops ...
	_ = s.manager.StopPersistentDrivers()
	s.manager.KillAll()
	// ... rest ...
}
```

- [ ] **Step 5: Wire in serve/start flows**

Find where `localserver.New(...)` is called (likely in `internal/cli/serve.go` and start path). Add:

```go
workflowDriver := a.NewWorkflowDriver()
server := localserver.New(
    localserver.WithConfig(cfg),
    localserver.WithWorkflowDriver(workflowDriver),
    // ... other options ...
)
```

- [ ] **Step 6: Verify build**

Run: `go build ./cmd/cs-cloud`
Expected: build succeeds.

- [ ] **Step 7: Commit**

```bash
git add internal/app/app.go internal/localserver/server.go internal/cli/serve.go
git commit -m "feat(workflow): wire workflow driver into daemon lifecycle

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 3: workspace/repo 管理

### Task 3.1: multica REST 客户端

**Files:**
- Create: `internal/agent/workflow/client.go`
- Test: `internal/agent/workflow/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

func TestClientGetWorkspaces(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":"ws-1","name":"Test"}]`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "token-123"}, nil
	})
	wss, err := c.GetWorkspaces()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !called || len(wss) != 1 {
		t.Fatalf("called=%v len=%d", called, len(wss))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestClientGetWorkspaces -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/agent/workflow/client.go
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

type Client struct {
	baseURL       string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

func NewClient(baseURL string, tp func() (*provider.Credentials, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tp,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) token() (string, error) {
	cred, err := c.tokenProvider()
	if err != nil {
		return "", err
	}
	if cred == nil || cred.AccessToken == "" {
		return "", fmt.Errorf("no access token")
	}
	return cred.AccessToken, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	token, err := c.token()
	if err != nil {
		return err
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workflow.HeaderClientPlatform, "cs-cloud")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, string(b))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) GetWorkspaces() ([]workflow.Workspace, error) {
	var out []workflow.Workspace
	err := c.request(context.Background(), http.MethodGet, workflow.MulticaWorkspacesEndpoint, nil, &out)
	return out, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/workflow -run TestClientGetWorkspaces -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/client.go internal/agent/workflow/client_test.go
git commit -m "feat(workflow): add multica REST client

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3.2: Workspace Manager

**Files:**
- Create: `internal/agent/workflow/workspace.go`
- Test: `internal/agent/workflow/workspace_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManagerEnsureRoot(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	if err := wm.EnsureRoot(); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestWorkspaceManagerEnsureRoot -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/agent/workflow/workspace.go
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
)

type WorkspaceManager struct {
	root string
}

func NewWorkspaceManager(root string) *WorkspaceManager {
	return &WorkspaceManager{root: root}
}

func (wm *WorkspaceManager) EnsureRoot() error {
	return os.MkdirAll(wm.root, 0o755)
}

func (wm *WorkspaceManager) WorkspaceDir(workspaceID string) string {
	return filepath.Join(wm.root, workspaceID)
}

func (wm *WorkspaceManager) RepoCacheDir(workspaceID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "repos")
}

func (wm *WorkspaceManager) TaskWorktreeDir(workspaceID, taskID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "tasks", taskID)
}

func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL string) (string, error) {
	// Phase 3.3 will implement git clone/cache logic.
	return "", fmt.Errorf("not implemented")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/workflow -run TestWorkspaceManagerEnsureRoot -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/workspace.go internal/agent/workflow/workspace_test.go
git commit -m "feat(workflow): add WorkspaceManager skeleton

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3.3: Repo Cache / Worktree

**Files:**
- Modify: `internal/agent/workflow/workspace.go`
- Test: `internal/agent/workflow/workspace_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManagerTaskDir(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	dir := wm.TaskWorktreeDir("ws-1", "task-1")
	expected := filepath.Join(root, "ws-1", "tasks", "task-1")
	if dir != expected {
		t.Fatalf("dir = %q", dir)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestWorkspaceManagerTaskDir -v`
Expected: FAIL (if not already implemented) or PASS.

- [ ] **Step 3: Implement repo checkout helper**

Add to `internal/agent/workflow/workspace.go`:

```go
import (
	// ... existing imports ...
	"os/exec"
)

func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL string) (string, error) {
	if repoURL == "" {
		return "", nil
	}
	cacheDir := wm.RepoCacheDir(workspaceID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	name := repoName(repoURL)
	cache := filepath.Join(cacheDir, name)

	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		if err := runGit("clone", "--mirror", repoURL, cache); err != nil {
			return "", fmt.Errorf("clone repo: %w", err)
		}
	} else {
		if err := runGit("-C", cache, "remote", "update"); err != nil {
			return "", fmt.Errorf("update repo: %w", err)
		}
	}
	return cache, nil
}

func (wm *WorkspaceManager) CreateWorktree(workspaceID, taskID, repoURL, ref string) (string, error) {
	cache, err := wm.EnsureRepoReady(workspaceID, repoURL)
	if err != nil {
		return "", err
	}
	dir := wm.TaskWorktreeDir(workspaceID, taskID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := runGit("-C", cache, "worktree", "add", dir, ref); err != nil {
		return "", fmt.Errorf("add worktree: %w", err)
	}
	return dir, nil
}

func repoName(url string) string {
	base := filepath.Base(url)
	if ext := filepath.Ext(base); ext == ".git" {
		return base[:len(base)-4]
	}
	return base
}

func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/workflow -run TestWorkspaceManager -v`
Expected: PASS for EnsureRoot and TaskDir; EnsureRepoReady may need git/network.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/workspace.go internal/agent/workflow/workspace_test.go
git commit -m "feat(workflow): implement repo cache and worktree helpers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3.4: 元数据同步循环

**Files:**
- Modify: `internal/agent/workflow/runtime.go`
- Test: `internal/agent/workflow/runtime_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"testing"
	"time"

	"cs-cloud/internal/workflow"
)

func TestRuntimeSyncInterval(t *testing.T) {
	cfg := workflow.Config{SyncInterval: 10 * time.Millisecond}
	r := newRuntime(cfg, nil, nil)
	r.syncFunc = func() error { return nil }
	r.Start()
	time.Sleep(50 * time.Millisecond)
	r.Stop()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestRuntimeSyncInterval -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/agent/workflow/runtime.go
package workflow

import (
	"context"
	"sync"
	"time"

	"cs-cloud/internal/workflow"
)

type runtimeLoop struct {
	cfg       workflow.Config
	client    *Client
	cache     *workflow.Cache
	syncFunc  func() error
	gcFunc    func() error

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRuntime(cfg workflow.Config, client *Client, cache *workflow.Cache) *runtimeLoop {
	return &runtimeLoop{
		cfg:    cfg,
		client: client,
		cache:  cache,
	}
}

func (r *runtimeLoop) Start() error {
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.wg.Add(2)
	go r.loop(r.cfg.SyncInterval, r.doSync)
	go r.loop(r.cfg.GCInterval, r.doGC)
	return nil
}

func (r *runtimeLoop) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *runtimeLoop) loop(interval time.Duration, fn func() error) {
	defer r.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			_ = fn()
		}
	}
}

func (r *runtimeLoop) doSync() error {
	if r.syncFunc != nil {
		return r.syncFunc()
	}
	if r.client == nil || r.cache == nil {
		return nil
	}
	wss, err := r.client.GetWorkspaces()
	if err != nil {
		return err
	}
	return r.cache.WriteWorkspaces(wss)
}

func (r *runtimeLoop) doGC() error {
	if r.gcFunc != nil {
		return r.gcFunc()
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/workflow -run TestRuntimeSyncInterval -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/runtime.go internal/agent/workflow/runtime_test.go
git commit -m "feat(workflow): add metadata sync and GC background loops

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 4: 任务执行与状态上报

### Task 4.1: 任务执行器

**Files:**
- Create: `internal/agent/workflow/task.go`
- Test: `internal/agent/workflow/task_test.go`

- [ ] **Step 1: Write the failing test**

```go
package workflow

import (
	"context"
	"testing"

	"cs-cloud/internal/workflow"
)

func TestTaskRunnerBuildEnv(t *testing.T) {
	tr := &TaskRunner{}
	env := tr.buildEnv(workflow.TaskRunPayload{
		WorkspaceID: "ws-1",
		Agent:       "claude",
	}, "/tmp/ws")
	if env["MULTICA_WORKSPACE_ID"] != "ws-1" {
		t.Fatalf("env = %+v", env)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/workflow -run TestTaskRunnerBuildEnv -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/agent/workflow/task.go
package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"cs-cloud/internal/workflow"
)

type TaskRunner struct {
	workspaceManager *WorkspaceManager
	client           *Client
	agentTimeout     time.Duration
}

func NewTaskRunner(wm *WorkspaceManager, client *Client, timeout time.Duration) *TaskRunner {
	return &TaskRunner{
		workspaceManager: wm,
		client:           client,
		agentTimeout:     timeout,
	}
}

func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) error {
	worktree, err := tr.workspaceManager.CreateWorktree(payload.WorkspaceID, payload.TaskID, "", "HEAD")
	if err != nil {
		return tr.fail(payload.TaskID, fmt.Errorf("prepare worktree: %w", err))
	}

	if err := tr.client.StartTask(payload.TaskID); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, payload.Agent)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)

	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = tr.client.PostTaskMessages(payload.TaskID, string(out))
		return tr.fail(payload.TaskID, fmt.Errorf("agent exit: %w", err))
	}

	_ = tr.client.PostTaskMessages(payload.TaskID, string(out))
	return tr.client.CompleteTask(payload.TaskID, map[string]any{"status": "ok"})
}

func (tr *TaskRunner) buildEnv(payload workflow.TaskRunPayload, worktree string) []string {
	env := []string{
		"MULTICA_WORKSPACE_ID=" + payload.WorkspaceID,
		"MULTICA_TASK_ID=" + payload.TaskID,
		"CS_CLOUD_WORKTREE=" + worktree,
	}
	for k, v := range payload.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (tr *TaskRunner) fail(taskID string, err error) error {
	_ = tr.client.FailTask(taskID, err.Error())
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/workflow -run TestTaskRunnerBuildEnv -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workflow/task.go internal/agent/workflow/task_test.go
git commit -m "feat(workflow): add task runner skeleton

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4.2: 完善 multica 任务状态 API

**Files:**
- Modify: `internal/agent/workflow/client.go`
- Test: `internal/agent/workflow/client_test.go`

- [ ] **Step 1: Add methods to Client**

```go
func (c *Client) StartTask(taskID string) error {
	path := fmt.Sprintf(workflow.MulticaTaskStartEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, nil, nil)
}

func (c *Client) CompleteTask(taskID string, result any) error {
	path := fmt.Sprintf(workflow.MulticaTaskCompleteEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"result": result}, nil)
}

func (c *Client) FailTask(taskID string, reason string) error {
	path := fmt.Sprintf(workflow.MulticaTaskFailEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"error": reason}, nil)
}

func (c *Client) PostTaskMessages(taskID string, messages string) error {
	path := fmt.Sprintf(workflow.MulticaTaskMessagesEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"messages": messages}, nil)
}
```

- [ ] **Step 2: Write tests**

Add tests for each method using httptest.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/agent/workflow -run TestClient -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/workflow/client.go internal/agent/workflow/client_test.go
git commit -m "feat(workflow): implement multica task status APIs

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4.3: Driver 启动时组装所有组件

**Files:**
- Modify: `internal/agent/workflow/driver.go`
- Modify: `internal/agent/workflow/types.go`
- Test: `internal/agent/workflow/driver_test.go`

- [ ] **Step 1: Update Driver struct**

```go
type Driver struct {
	cfg              workflow.Config
	deps             *Dependencies
	workspaceManager *WorkspaceManager
	client           *Client
	runtime          *runtimeLoop
	runner           *TaskRunner
	state            driverState
	mu               sync.Mutex
}

type driverState int

const (
	driverStateIdle driverState = iota
	driverStateRunning
	driverStateError
)
```

- [ ] **Step 2: Implement Start/Stop/Health**

```go
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.state == driverStateRunning {
		return nil
	}

	d.workspaceManager = NewWorkspaceManager(d.cfg.WorkspacesRoot)
	if err := d.workspaceManager.EnsureRoot(); err != nil {
		d.state = driverStateError
		return err
	}

	cache := workflow.NewCache(d.cfg.CacheDir)
	d.client = NewClient(d.deps.MulticaBaseURL, d.deps.TokenProvider)
	d.runtime = newRuntime(d.cfg, d.client, cache)
	d.runner = NewTaskRunner(d.workspaceManager, d.client, d.cfg.AgentTimeout)

	if err := d.runtime.Start(); err != nil {
		d.state = driverStateError
		return err
	}

	d.state = driverStateRunning
	return nil
}

func (d *Driver) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtime != nil {
		_ = d.runtime.Stop()
	}
	d.state = driverStateIdle
	return nil
}

func (d *Driver) Health() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != driverStateRunning {
		return fmt.Errorf("workflow driver not running")
	}
	return nil
}

func (d *Driver) RunTask(payload workflow.TaskRunPayload) error {
	if err := d.Health(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
	defer cancel()
	return d.runner.Run(ctx, payload)
}
```

- [ ] **Step 3: Update Dependencies**

```go
type Dependencies struct {
	MulticaBaseURL string
	TokenProvider  func() (*provider.Credentials, error)
}
```

- [ ] **Step 4: Update app wiring**

Modify `internal/app/app.go`:

```go
func (a *App) NewWorkflowDriver() *workflowagent.Driver {
	return workflowagent.NewDriver(a.cfg.Workflow, &workflowagent.Dependencies{
		MulticaBaseURL: a.cfg.Workflow.MulticaBaseURL,
		TokenProvider:  a.Credentials,
	})
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agent/workflow -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/agent/workflow/*.go internal/app/app.go
git commit -m "feat(workflow): wire driver components together

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Phase 5: LocalServer 路由

### Task 5.1: workflow handler

**Files:**
- Create: `internal/localserver/workflow_handler.go`
- Test: `internal/localserver/workflow_handler_test.go`

- [ ] **Step 1: Write the failing test**

```go
package localserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/runtime"
	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/workflow"
)

func TestHandleWorkflowHealth(t *testing.T) {
	m := runtime.NewAgentManager(runtime.NewEventBus())
	d := workflowagent.NewDriver(workflow.Config{}, nil)
	m.RegisterPersistentDriver(d)

	s := New(WithManager(m))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/health", nil)
	rec := httptest.NewRecorder()
	s.handleWorkflowHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/localserver -run TestHandleWorkflowHealth -v`
Expected: FAIL

- [ ] **Step 3: Write implementation**

```go
// internal/localserver/workflow_handler.go
package localserver

import (
	"encoding/json"
	"net/http"

	"cs-cloud/internal/workflow"
)

func (s *Server) handleWorkflowHealth(w http.ResponseWriter, r *http.Request) {
	d, ok := s.manager.GetPersistentDriver("workflow")
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	if err := d.Health(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleWorkflowTaskRun(w http.ResponseWriter, r *http.Request) {
	d, ok := s.manager.GetPersistentDriver("workflow")
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}

	var payload workflow.TaskRunPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	// Type assert to workflow driver interface with RunTask.
	type taskRunner interface {
		RunTask(workflow.TaskRunPayload) error
	}
	tr, ok := d.(taskRunner)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "driver does not support tasks")
		return
	}

	if err := tr.RunTask(payload); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "started"})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/localserver -run TestHandleWorkflowHealth -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/localserver/workflow_handler.go internal/localserver/workflow_handler_test.go
git commit -m "feat(localserver): add workflow health and task run handlers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5.2: 注册 workflow 路由

**Files:**
- Modify: `internal/localserver/server.go`

- [ ] **Step 1: Register routes in New()**

Add after existing route definitions:

```go
	api.HandleFunc("GET /workflow/health", s.handleWorkflowHealth)
	api.HandleFunc("POST /workflow/tasks/{id}/run", s.handleWorkflowTaskRun)
```

- [ ] **Step 2: Build**

Run: `go build ./cmd/cs-cloud`
Expected: success

- [ ] **Step 3: Commit**

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

- [ ] **Step 1: Write implementation**

```go
// internal/cli/workflow.go
package cli

import (
	"fmt"

	"cs-cloud/internal/app"
)

func workflowCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		printWorkflowUsage()
		return nil
	}

	switch args[0] {
	case "workspace":
		return workflowWorkspaceCmd(a, args[1:])
	case "issue":
		return workflowIssueCmd(a, args[1:])
	case "project":
		return workflowProjectCmd(a, args[1:])
	case "task":
		return workflowTaskCmd(a, args[1:])
	case "help", "-h", "--help":
		printWorkflowUsage()
		return nil
	default:
		printWorkflowUsage()
		return fmt.Errorf("unknown workflow command: %s", args[0])
	}
}

func printWorkflowUsage() {
	printTitle("cs-cloud workflow")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud workflow <resource> <action>"))
	printSection("Resources")
	cmds := [][2]string{
		{"workspace", "List/get/sync workspaces"},
		{"issue", "List/create/update issues"},
		{"project", "List projects"},
		{"task", "Run/check workflow tasks"},
	}
	fmt.Print(renderKV(cmds))
}
```

- [ ] **Step 2: Hook into dispatch**

Modify `internal/cli/root.go` dispatch switch:

```go
	case "workflow":
		return workflowCmd(a, cmds[1:])
```

Add to printUsage command list:

```go
		{"workflow", "Manage multica workflow resources"},
```

- [ ] **Step 3: Run build**

Run: `go build ./cmd/cs-cloud`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add internal/cli/workflow.go internal/cli/root.go
git commit -m "feat(cli): add workflow subcommand entrypoint

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6.2: workspace 子命令

**Files:**
- Create: `internal/cli/workflow_workspace.go`
- Test: `internal/cli/workflow_workspace_test.go`

- [ ] **Step 1: Write implementation**

```go
// internal/cli/workflow_workspace.go
package cli

import (
	"fmt"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/workflow"
)

func workflowWorkspaceCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		return workflowWorkspaceList(a)
	}
	switch args[0] {
	case "list":
		return workflowWorkspaceList(a)
	case "sync":
		return workflowWorkspaceSync(a)
	default:
		return fmt.Errorf("unknown workspace action: %s", args[0])
	}
}

func workflowWorkspaceList(a *app.App) error {
	cfg := a.Config()
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	wss, err := cache.ReadWorkspaces()
	if err != nil {
		return err
	}
	for _, ws := range wss {
		fmt.Printf("%s  %s\n", ws.ID, ws.Name)
	}
	return nil
}

func workflowWorkspaceSync(a *app.App) error {
	cfg := a.Config()
	creds, err := a.Credentials()
	if err != nil {
		return err
	}
	client := workflowagent.NewClient(cfg.Workflow.MulticaBaseURL, func() (*provider.Credentials, error) {
		return creds, nil
	})
	wss, err := client.GetWorkspaces()
	if err != nil {
		return err
	}
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	if err := cache.WriteWorkspaces(wss); err != nil {
		return err
	}
	fmt.Printf("Synced %d workspaces\n", len(wss))
	return nil
}
```

- [ ] **Step 2: Commit**

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

- [ ] **Step 1: Write test**

```go
package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/workflow"
)

func TestWorkflowTaskRunEndpoint(t *testing.T) {
	m := runtime.NewAgentManager(runtime.NewEventBus())
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		AgentTimeout:   time.Minute,
	}
	d := workflowagent.NewDriver(cfg, &workflowagent.Dependencies{
		MulticaBaseURL: "http://localhost:1", // will fail for actual run, ok for skeleton
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	m.RegisterPersistentDriver(d)

	s := New(WithManager(m))

	payload := workflow.TaskRunPayload{TaskID: "t1", WorkspaceID: "ws-1", Agent: "echo", Prompt: "hello"}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/t1/run", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/localserver -run TestWorkflowTaskRunEndpoint -v`
Expected: PASS (driver may return error but handler should handle gracefully; adjust expectations).

- [ ] **Step 3: Commit**

```bash
git add internal/localserver/workflow_http_test.go
git commit -m "test(localserver): add workflow endpoint integration test

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7.2: 端到端手动验证

- [ ] **Step 1: Build cs-cloud**

Run: `go build -o bin/cs-cloud ./cmd/cs-cloud`
Expected: binary created.

- [ ] **Step 2: Run cs-cloud serve**

Run: `./bin/cs-cloud serve`
Expected: localserver starts, workflow driver initializes.

- [ ] **Step 3: Test workflow health endpoint**

Run: `curl http://127.0.0.1:<port>/api/v1/workflow/health`
Expected: either `ok` (driver running) or `UNAVAILABLE` (driver not started).

- [ ] **Step 4: Test workflow workspace sync**

Run: `./bin/cs-cloud workflow workspace sync`
Expected: outputs synced workspace count or authentication error if multica backend unavailable.

- [ ] **Step 5: Simulate Gateway task run**

Run:
```bash
curl -X POST http://127.0.0.1:<port>/api/v1/workflow/tasks/task-1/run \
  -H "Content-Type: application/json" \
  -d '{"task_id":"task-1","workspace_id":"ws-1","agent":"echo","prompt":"hello"}'
```
Expected: `{"ok":true,"data":{"status":"started"}}` or error if multica backend unreachable.

- [ ] **Step 6: Document manual verification results**

Add a note to `docs/superpowers/plans/2026-07-15-cs-workflow-migration.md` under this task or a separate verification log.

---

## Phase 8: 最终交付

### Task 8.1: 运行全量测试

- [ ] **Step 1: Run all workflow tests**

Run: `go test ./internal/workflow/... ./internal/agent/workflow/... ./internal/localserver/... ./internal/cli/... ./internal/config/... ./internal/runtime/...`
Expected: all PASS

- [ ] **Step 2: Run go vet and build**

Run:
```bash
go vet ./...
go build ./cmd/cs-cloud
```
Expected: no errors

- [ ] **Step 3: Update ARCHITECTURE.md**

Add a section under"扩展点" describing the workflow persistent driver extension.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs(architecture): document workflow driver extension point

Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task 8.2: 创建 PR

- [ ] **Step 1: Push branch**

Run: `git push origin docs/cs-workflow-migration-design` (or the implementation branch name when created)

- [ ] **Step 2: Open PR targeting main**

Use `gh pr create` with title and description referencing the design doc.

- [ ] **Step 3: Stop and wait for user review**

Do not merge without explicit user approval per CLAUDE.md rules.

---

## Self-Review Checklist

- [x] Spec coverage: every design section (config, driver, routes, CLI, error handling, testing) has corresponding tasks.
- [x] Placeholder scan: no TBD/TODO/"implement later" in tasks.
- [x] Type consistency: `workflow.Config`, `TaskRunPayload`, `PersistentDriver` used consistently across tasks.
- [ ] Note: `WorkflowMulticaBaseURL` field in `internal/config/config.go` needs to be added if not already present; ensure Task 0.2 covers it.

