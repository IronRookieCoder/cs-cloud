# cs-workflow 客户端能力迁移到 cs-cloud 设计文档

> 日期：2026-07-15  
> 状态：待实现评审  
> 关联评估：`/Users/linkai/code/multica/CS-CLOUD_迁移评估.xlsx`

---

## 1. 背景与目标

### 1.1 背景

- `cs-cloud` 是 CoStrict 生态下的本地设备代理守护进程，通过 WebSocket + yamux 隧道接入 CoStrict Gateway，本地暴露 REST Control Plane 供云端反向调用。
- `cs-workflow`（位于 `/Users/linkai/code/multica/server/cmd/cs-workflow`）是 multica 原生的本地客户端/守护进程，直连 multica 后端，主动轮询任务并调度本地 AI Agent CLI 执行。
- 根据迁移评估，两者同属本地客户端/桥接程序，但归属不同生态，业务模型与通信架构差异较大。

### 1.2 目标

将 `cs-workflow` 的核心能力（任务执行、工作区管理、仓库缓存、GC、业务 CLI）迁移到 `cs-cloud` 中，作为其一个新增子系统，同时：

1. **尽量隔离代码**：新增的 workflow 能力与 cs-cloud 核心代码边界清晰，便于独立维护。
2. **适配 cs-cloud Gateway**：CoStrict 云端通过现有 Gateway 隧道反向调用 cs-cloud 本地 API 来触发 workflow 任务。
3. **保留 multica 后端兼容性**：workflow 任务执行、状态上报、业务查询继续与 multica 后端交互。
4. **替换原 cs-workflow 二进制**：用户最终只使用 `cs-cloud` 及其子命令完成原 workflow 相关操作。

---

## 2. 关键决策

通过需求澄清，确认以下决策：

| # | 问题 | 选择 |
|---|------|------|
| 1 | 移植方式 | **C**：把 cs-workflow 核心能力迁移进 cs-cloud，作为新子系统并保留 multica 后端兼容性 |
| 2 | 与 cs-cloud 集成方式 | **A**：新增一个 Agent Driver 类型（`internal/agent/workflow`），由 runtime manager 调度 |
| 3 | 认证方式 | **B**：CoStrict token 透传，workflow driver 复用 `~/.costrict/share/auth.json` 中的 token 调用 multica 后端 |
| 4 | Gateway 适配 | **B**：在 cs-cloud `internal/localserver` 新增 `/api/v1/workflow/*` 路由，云端通过 Gateway 隧道调用 |
| 5 | 任务调度 | **B**：改为云端推送，multica 后端同步任务给 CoStrict 云端，再由云端通过 Gateway 调用 cs-cloud 触发执行；driver 不再主动轮询 |
| 6 | 工作区管理 | **A**：完全保留 multica 工作区模型，本地维护 `~/multica_workspaces` 等价目录、仓库缓存与 GC |
| 7 | CLI 命令 | **A**：作为 cs-cloud 子命令（`cs-cloud workflow workspace list` 等） |
| 8 | 结果回传 | **A**：任务执行结果直接回传 multica 后端 |
| 9 | 业务元数据 | **A**：本地缓存 + 同步，workspace/issue/project 等元数据在 cs-cloud 本地缓存并定期同步 |
| 10 | 与原 cs-workflow 兼容性 | **A**：完全替代，不保证并行，数据目录写入新位置 |

---

## 3. 总体架构与模块边界

### 3.1 高层架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        CoStrict Cloud / Web 控制台                   │
│                              │                                      │
│                              ▼                                      │
│              ┌──────────────────────────────┐                      │
│              │   Gateway (costrict-web)     │                      │
│              │   /device/:id/proxy/...      │                      │
│              └──────────────┬───────────────┘                      │
└─────────────────────────────┼──────────────────────────────────────┘
                              │ WebSocket + yamux 隧道
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         cs-cloud daemon (本地)                       │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │                  internal/localserver                         │  │
│  │              /api/v1/workflow/* 路由                          │  │
│  └──────────────────────────┬───────────────────────────────────┘  │
│                             │                                       │
│  ┌──────────────────────────▼───────────────────────────────────┐  │
│  │                  internal/runtime/manager                     │  │
│  │              启动/停止 workflow driver                        │  │
│  └──────────────────────────┬───────────────────────────────────┘  │
│                             │                                       │
│  ┌──────────────────────────▼───────────────────────────────────┐  │
│  │              internal/agent/workflow (driver)                 │  │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────────────┐     │  │
│  │  │   Gateway   │ │  Workspace  │ │   Multica Client    │     │  │
│  │  │   Handler   │ │  Manager    │ │   (REST + WS wakeup)│     │  │
│  │  └─────────────┘ └─────────────┘ └─────────────────────┘     │  │
│  └──────────────────────────┬───────────────────────────────────┘  │
│                             │ HTTP/HTTPS (CoStrict token)          │
└─────────────────────────────┼──────────────────────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    multica 后端（保留的服务端）                       │
│              /api/daemon/* /api/workspaces/* /api/issues/*          │
└─────────────────────────────────────────────────────────────────────┘
```

### 3.2 模块边界

| 模块 | 路径 | 职责 | 依赖 |
|------|------|------|------|
| **workflow driver** | `internal/agent/workflow` | 任务接收、工作区管理、Agent 执行、状态上报 | `internal/runtime`, `internal/config`, `internal/model`, `internal/logger` |
| **workflow API handlers** | `internal/localserver/handlers/workflow.go` | 暴露 `/api/v1/workflow/*` 路由，把 Gateway 请求转给 driver | `internal/agent/workflow`, `internal/localserver` |
| **workflow CLI** | `internal/cli/workflow.go` | `cs-cloud workflow *` 子命令 | `internal/agent/workflow`, `internal/cli` |
| **workflow 共享层** | `internal/workflow/` | 类型定义、配置、本地缓存、multica 协议常量 | `internal/model`, `internal/config` |
| **runtime manager 扩展** | `internal/runtime/manager.go` | 支持常驻型 driver 的启动/停止/健康检查 | `internal/agent/workflow` |

### 3.3 关键设计原则

1. **单向依赖**：`workflow` 包可以依赖 cs-cloud 底层（config/model/logger/platform），但 cs-cloud 核心模块不直接依赖 workflow 内部细节。
2. **driver 接口隔离**：workflow driver 实现 `internal/agent/driver.go` 定义的接口，runtime manager 通过接口操作。
3. **配置目录隔离**：workflow 本地状态写入 `~/.costrict/cs-cloud/workflow/`，不污染 `~/.multica/`。
4. **Gateway 无侵入**：不修改 Gateway 代码，仅利用其现有 `/device/:deviceID/proxy/*path` 能力反向调用 cs-cloud 本地 API。

---

## 4. 新增/修改的组件

### 4.1 `internal/agent/workflow` — workflow driver 实现

#### `driver.go`
- 实现 `internal/agent/driver.go` 中的 `Driver` 接口。
- 声明 driver 名称为 `"workflow"`。
- 关键方法：
  - `Start(ctx, opts) error`：启动 workflow runtime。
  - `Stop(ctx) error`：停止 workflow runtime，清理任务。
  - `Health() error`：检查 runtime 健康状态。
  - `ProxyRoutes() []ProxyRoute`：返回需要 localserver 代理到 driver 内部 HTTP server 的路由（可选）。

#### `runtime.go`
- 长生命周期 runtime，负责：
  - 维护与 multica 后端的 WebSocket wakeup 连接（可选，用于后端主动唤醒）。
  - 监听来自 localserver 的任务命令通道。
  - 维护当前活跃任务表。
  - 定期心跳/同步 workspace 元数据到本地缓存。

#### `workspace.go`
- 工作区管理器：
  - 维护 `~/.costrict/cs-cloud/workflow/workspaces/` 目录。
  - 仓库检出、缓存、worktree 管理。
  - GC 策略（按 workspace / task / 时间）。
  - 与原 `server/internal/daemon/repocache` 功能对齐，但适配新目录结构。

#### `task.go`
- 任务执行器：
  - 解析云端下发的 task payload。
  - 准备执行环境（env、cwd、repo）。
  - 调用本地 Agent CLI（复用 `internal/agent` 的 exec helper 或自研 wrapper）。
  - 收集 stdout/stderr/SSE 事件，实时上报 multica 后端。

#### `client.go`
- multica 后端 REST 客户端：
  - 封装 `POST /api/daemon/tasks/:id/start|complete|fail|usage|messages|session` 等。
  - 封装 workspace/issue/project 查询 API（供 CLI 使用）。
  - 统一附加 `Authorization: Bearer <costrict_token>` 和 identity headers。
  - 复用 cs-cloud 的 token 刷新逻辑。

### 4.2 `internal/localserver/handlers/workflow.go` — Gateway 入口

新增路由（前缀 `/api/v1/workflow`）：

| 方法 | 路径 | 作用 |
|------|------|------|
| POST | `/workflow/start` | 启动 workflow driver |
| POST | `/workflow/stop` | 停止 workflow driver |
| GET  | `/workflow/health` | driver 健康检查 |
| POST | `/workflow/tasks/:id/run` | 触发执行一个 workflow 任务 |
| POST | `/workflow/tasks/:id/abort` | 中止任务 |
| GET  | `/workflow/workspaces` | 列出本地缓存的 workspace |
| POST | `/workflow/workspaces/:id/sync` | 强制同步 workspace 元数据 |
| POST | `/workflow/gc` | 触发工作区 GC |

Handler 职责：
- 参数校验。
- 调用 runtime manager 获取 workflow driver 实例。
- 把请求转给 driver 的方法。
- 返回标准 `{ok, data, error}` 响应。

### 4.3 `internal/cli/workflow.go` — 用户 CLI

新增 `cs-cloud workflow` 子命令树：

```
cs-cloud workflow
├── workspace
│   ├── list
│   ├── get <id>
│   └── sync
├── issue
│   ├── list
│   ├── create
│   └── update <id>
├── project
│   ├── list
│   └── get <id>
├── autopilot
│   ├── list
│   └── trigger <id>
├── repo
│   └── checkout
└── task
    ├── run <id>
    └── status <id>
```

实现：
- 直接调用 multica 后端 API（复用 `internal/agent/workflow/client.go`）。
- 读取 `~/.costrict/share/auth.json` 获取 token。
- 输出格式支持 table / json（与原 cs-workflow 对齐关键命令）。

### 4.4 `internal/workflow` — 共享层（新增）

存放跨 driver / handler / cli 共享的内容：

- `config.go`：workflow 配置结构（workspaces root、GC 参数、同步间隔）。
- `models.go`：workspace、issue、project、task 等 DTO。
- `cache.go`：本地缓存读写，MVP 阶段使用 JSON 文件，后续可替换为 bbolt/SQLite。
- `protocol.go`：multica 后端 API 路径常量、请求/响应类型。

### 4.5 `internal/runtime/manager.go` — 扩展

当前 runtime manager 主要按需启动 agent CLI 进程。需要扩展以支持常驻 driver：

- 新增 `PersistentDrivers []string` 配置，启动时拉起。
- `RegisterPersistentDriver(name string, factory PersistentDriverFactory)`。
- `GetDriver(name string) (Driver, error)` 供 handler 调用。
- workflow driver 是首个常驻 driver。

### 4.6 `internal/config` — 配置扩展

- 新增 `WorkflowConfig` 字段。
- 支持环境变量：`CS_CLOUD_WORKFLOW_WORKSPACES_ROOT`、`CS_CLOUD_WORKFLOW_SYNC_INTERVAL` 等。
- 默认值：
  - workspaces root：`~/.costrict/cs-cloud/workflow/workspaces`
  - cache dir：`~/.costrict/cs-cloud/workflow/cache`
  - sync interval：5m
  - GC interval：24h

---

## 5. 关键数据流

### 5.1 cs-cloud 启动并拉起 workflow driver

```
cs-cloud start
    │
    ▼
app.Start()
    │
    ▼
config.Load() ──► 读取 WorkflowConfig（环境变量 + 默认值）
    │
    ▼
runtime.NewManager(cfg, persistentDrivers: ["workflow"])
    │
    ▼
manager.StartPersistentDrivers()
    │
    ▼
workflow.Driver.Start(ctx, opts)
    │
    ├──► workspace manager 初始化 workspaces root / cache dir
    │
    ├──► multica client 读取 ~/.costrict/share/auth.json token
    │
    ├──► 启动后台 goroutine：workspace 元数据同步、GC、心跳
    │
    └──► （未来可选）启动 WebSocket wakeup 连接到 multica 后端；MVP 阶段依赖 CoStrict 云端推送，不实现 wakeup
```

### 5.2 CoStrict 云端通过 Gateway 触发任务

```
CoStrict Web 控制台 / 调度服务
    │
    ▼
costrict-web Server
    │
    ▼
POST /internal/device/:deviceID/proxy/api/v1/workflow/tasks/:id/run
    │
    ▼
Gateway ──► 在 device 的 yamux 隧道中打开 stream
    │
    ▼
cs-cloud localserver /api/v1/workflow/tasks/:id/run handler
    │
    ▼
runtime.manager.GetDriver("workflow")
    │
    ▼
workflow.Driver.RunTask(taskID, payload)
    │
    ▼
task executor
```

### 5.3 任务执行与状态上报

```
task executor
    │
    ├──► 从 payload 解析 workspace_id、issue_id、agent_provider、prompt 等
    │
    ├──► workspaceManager.EnsureRepoReady(workspaceID, repoURL)
    │         │
    │         ├──► 命中 repo cache ──► 复用 worktree
    │         └──► 未命中 ──► git clone / fetch ──► 创建 worktree
    │
    ├──► 构建本地 Agent CLI 命令（claude/codex/...）
    │         │
    │         ├──► 复用 internal/agent 的 execenv wrapper
    │         └──► 注入 MULTICA_WORKSPACE_ID、COSTRICT_TOKEN 等 env
    │
    ├──► 启动 Agent 子进程
    │         │
    │         ├──► stdout/stderr ──► 实时解析 SSE / JSON 事件
    │         └──► 事件 ──► multicaClient.PostTaskMessages(taskID, events)
    │
    ├──► 任务完成/失败
    │         │
    │         ├──► multicaClient.PostTaskComplete(taskID, result)
    │         └──► 或 multicaClient.PostTaskFail(taskID, err)
    │
    └──► usage 上报 ──► multicaClient.PostTaskUsage(taskID, usage)
```

### 5.4 `cs-cloud workflow` CLI 调用 multica 后端

```
cs-cloud workflow workspace list
    │
    ▼
internal/cli/workflow.go
    │
    ▼
workflow.Client.GetWorkspaces() 或 读取本地 cache
    │
    ▼
HTTP GET https://<multica-server>/api/workspaces
    │
    ▼
Authorization: Bearer <costrict_token>
    │
    ▼
multica 后端
```

### 5.5 workspace 元数据同步与缓存

```
workflow driver 后台 goroutine（每 5m）
    │
    ▼
multicaClient.GetWorkspaces()
    │
    ▼
写入 ~/.costrict/cs-cloud/workflow/cache/workspaces.json
    │
    ▼
CLI `cs-cloud workflow workspace list` 优先读缓存
    │
    ▼
--force-sync 或缓存过期时重新拉取
```

---

## 6. 错误处理与重试策略

### 6.1 启动失败

| 场景 | 行为 |
|------|------|
| workflow driver 启动失败 | runtime manager 记录错误，不影响 cs-cloud 其他模块启动；`cs-cloud doctor` 报告 workflow 状态为 degraded |
| token 缺失/无效 | driver 进入 `unauthenticated` 状态，等待 `cs-cloud login` 后重试；CLI 命令返回明确错误 |
| multica 后端不可达 | driver 按指数退避重连，max 5m；标记状态为 `backend_unavailable` |

### 6.2 Gateway 请求失败

| HTTP 状态 | 处理 |
|-----------|------|
| 200 OK | 正常返回 |
| 400 Bad Request | 返回错误 envelope，云端修正 payload |
| 404 Not Found | driver 未启动或路由未注册，云端可选择先调用 `/workflow/start` |
| 409 Conflict | 任务已在运行或 driver 正在停止，返回当前任务状态 |
| 500 Internal | 记录日志，返回错误，不自动重试（由云端侧决定是否重试） |
| 503 Unavailable | workflow driver 未就绪，建议云端延迟重试 |

### 6.3 multica 后端调用失败

| 场景 | 策略 |
|------|------|
| 401/403 token 失效 | 触发 token 刷新；刷新失败则进入 `unauthenticated` 状态 |
| 404 task/workspace/runtime not found | 终止当前任务，清理本地状态，不尝试重连该资源 |
| 5xx / 网络超时 | 指数退避重试，最多 5 次；超过则标记任务失败 |
| 429 rate limit | 读取 `Retry-After` 或默认 60s 后重试 |

### 6.4 任务执行失败

| 阶段 | 失败处理 |
|------|----------|
| repo 准备失败 | 上报 `task fail` 并附带错误详情；不保留不完整 worktree |
| Agent CLI 启动失败 | 上报 `task fail`；记录命令和环境用于排查 |
| Agent 运行中崩溃 | 捕获退出码，上报 `task fail`；stdout/stderr 已上报部分保留 |
| 任务被 abort | 发送 SIGTERM → 5s 后 SIGKILL；上报 `task fail` reason=aborted |
| 任务执行超时 | 按 `agent_timeout` 配置 kill 进程；上报 `task fail` reason=timeout |

### 6.5 WebSocket wakeup 断开

- 断开后立即进入 polling fallback（每 30s 查询一次任务状态/待执行任务）。
- 同时尝试重连 WebSocket，指数退避 max 5m。
- 重连成功后切回 WebSocket 模式。

### 6.6 认证失败/token 过期

- 统一使用 `internal/provider` 的 token 刷新机制。
- 若 refresh 失败：
  - driver 进入 `unauthenticated` 状态。
  - CLI 命令提示用户重新执行 `cs-cloud login`。
  - Gateway 路由返回 401，云端可触发用户重新授权流程。

### 6.7 workspace/repo 操作失败

| 场景 | 处理 |
|------|------|
| git clone 失败 | 清理临时目录，标记 workspace `repo_unavailable`，上报任务失败 |
| worktree 创建冲突 | 使用唯一 suffix 重新创建，旧 worktree 按 GC 策略清理 |
| 磁盘空间不足 | GC 立即触发；若仍不足，拒绝新任务并上报 `resource_exhausted` |
| repo cache 损坏 | 删除并重新 clone |

### 6.8 GC 失败

- GC 失败不阻塞任务执行。
- 记录 error 日志，metrics 暴露 `workflow_gc_failures_total`。
- 下次 GC 周期重试。

---

## 7. 测试策略

### 7.1 单元测试

| 测试文件 | 覆盖内容 |
|----------|----------|
| `internal/agent/workflow/*_test.go` | driver 启动/停止、任务解析、状态机转换 |
| `internal/agent/workflow/client_test.go` | multica 客户端请求构造、token 注入、错误分类 |
| `internal/agent/workflow/workspace_test.go` | repo cache 命中/未命中、worktree 命名、GC 策略 |
| `internal/localserver/handlers/workflow_test.go` | 路由参数校验、driver 未启动时返回 503、success/fail envelope |
| `internal/cli/workflow_test.go` | 命令行参数解析、输出格式（table/json） |
| `internal/workflow/cache_test.go` | 缓存读写、过期、并发安全 |

### 7.2 集成测试

| 测试文件 | 覆盖内容 |
|----------|----------|
| `internal/agent/workflow/runtime_integration_test.go` | 使用 fake multica server（httptest）验证：注册、心跳、任务触发、状态上报 |
| `internal/localserver/workflow_http_test.go` | 启动完整 localserver + fake driver，验证 Gateway 调用链路 |
| `internal/agent/workflow/exec_integration_test.go` | 用 dummy agent CLI 验证任务执行、stdout 收集、abort 信号 |

### 7.3 端到端/手动验证

| 场景 | 验证方式 |
|------|----------|
| `cs-cloud start` 自动拉起 workflow driver | 本地运行，查看日志/状态 |
| Gateway 触发任务 | 通过 CoStrict 云端或 curl 模拟 Gateway proxy 调用 |
| 任务执行并上报 multica | 在 fake multica server 上断言接收到的 start/complete/messages |
| `cs-cloud workflow workspace list` | 验证 CLI 输出与 multica 后端一致 |
| repo checkout / GC | 构造多个 workspace，验证缓存和清理行为 |
| token 刷新失败 | 修改 auth.json 过期时间，验证降级行为 |

### 7.4 测试隔离

- 所有 workflow 测试使用临时目录（`t.TempDir()`），不触碰真实 `~/.costrict/` 或 `~/.multica/`。
- multica 后端用 `httptest.Server` 模拟，避免依赖真实网络。
- agent CLI 用 shell 脚本或 Go 小工具模拟，避免依赖真实 claude/codex 二进制。

### 7.5 CI 覆盖

- 新增 `go test ./internal/agent/workflow/... ./internal/cli/... ./internal/localserver/...` 到 CI。
- 跨平台测试：Linux/macOS/Windows 至少各跑一次单元测试。

---

## 8. 配置与目录结构

### 8.1 新增配置项

| 环境变量 | 配置字段 | 默认值 | 说明 |
|----------|----------|--------|------|
| `CS_CLOUD_WORKFLOW_WORKSPACES_ROOT` | `Workflow.WorkspacesRoot` | `~/.costrict/cs-cloud/workflow/workspaces` | 工作区根目录 |
| `CS_CLOUD_WORKFLOW_CACHE_DIR` | `Workflow.CacheDir` | `~/.costrict/cs-cloud/workflow/cache` | 本地缓存目录 |
| `CS_CLOUD_WORKFLOW_SYNC_INTERVAL` | `Workflow.SyncInterval` | `5m` | workspace 元数据同步间隔 |
| `CS_CLOUD_WORKFLOW_GC_INTERVAL` | `Workflow.GCInterval` | `24h` | GC 执行间隔 |
| `CS_CLOUD_WORKFLOW_AGENT_TIMEOUT` | `Workflow.AgentTimeout` | `30m` | 单个任务超时 |
| `CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS` | `Workflow.MaxConcurrentTasks` | `20` | 最大并发任务数 |

### 8.2 目录结构

```
~/.costrict/cs-cloud/workflow/
├── workspaces/           # 工作区目录（等价于原 ~/multica_workspaces）
│   ├── <workspace-id>/
│   │   ├── repos/        # repo cache
│   │   └── tasks/        # 任务级 worktree
└── cache/
    ├── workspaces.json   # workspace 元数据缓存
    ├── issues.json       # issue 缓存（可选）
    └── state.json        # driver 运行状态
```

> **日志**：workflow 相关日志统一写入 cs-cloud 日志（`~/.costrict/cs-cloud/logs/`），不单独维护 workflow.log。未来若流量过大再考虑分离。

---

## 9. 风险与待确认事项

### 9.1 高风险

1. **CoStrict token 能否被 multica 后端识别？**  
   当前设计假设 CoStrict OAuth token 可以直接用于 multica 后端。若该假设不成立，需要改为双 token 模式或云端代发 token。

2. **multica 后端任务如何同步到 CoStrict 云端？**  
   "云端推送"模式依赖 CoStrict 云端侧有桥接/同步 multica 任务的逻辑。该部分不在 cs-cloud 范围内，需与 CoStrict 云端团队确认接口契约。

3. **常驻 driver 对 runtime manager 的影响**  
   当前 runtime manager 主要管理按需启动的 agent 进程。扩展为支持常驻 driver 需要谨慎设计，避免影响现有 csc/cs/acp driver 的行为。

### 9.2 中风险

1. **仓库缓存/GC 逻辑移植复杂度**  
   原 `server/internal/daemon/repocache` 和 GC 逻辑与 multica 业务模型紧密耦合，移植时需要剥离 multica 特有概念。

2. **跨平台兼容性**  
   workspace、repo、Agent CLI 路径在不同 OS 上可能有差异，需要充分测试 Windows 行为。

3. **CLI 命令兼容性**  
   用户可能习惯了原 cs-workflow 命令，子命令命名和输出格式需要尽量对齐关键路径。

### 9.3 待确认

1. multica 后端当前部署地址（`MULTICA_SERVER_URL`）以及是否通过 costrict gateway 访问。
2. CoStrict 云端下发 workflow 任务的具体 payload 格式。
3. 是否需要保留 WebSocket wakeup 机制，还是完全依赖云端推送。
4. 本地缓存的存储格式偏好（JSON、bbolt、SQLite）。
5. 工作区 GC 的具体策略（按时间、按大小、按任务完成状态）。

---

## 10. 下一步

本设计文档通过评审后，进入 `writing-plans` 阶段，输出详细实施计划，包括：

1. 第一阶段：基础设施（目录结构、配置、共享类型、multica client）。
2. 第二阶段：runtime manager 扩展 + workflow driver 骨架。
3. 第三阶段：workspace/repo 管理 + GC。
4. 第四阶段：任务执行 + 状态上报。
5. 第五阶段：localserver 路由 + CLI 子命令。
6. 第六阶段：测试、文档、端到端验证。
