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
| **workflow API handlers** | `internal/localserver/workflow_handler.go` | 暴露 `/api/v1/workflow/*` 路由，把 Gateway 请求转给 driver | `internal/agent/workflow`, `internal/localserver` |
| **workflow CLI** | `internal/cli/workflow.go`, `internal/cli/workflow_workspace.go` | `cs-cloud workflow *` 子命令 | `internal/agent/workflow`, `internal/cli` |
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
- 实现 `internal/agent/workflow/driver.go` 中的 `Driver` 类型，由 `internal/localserver/server.go` 直接持有并管理生命周期。
- 声明 driver 名称为 `"workflow"`。
- 关键方法：
  - `Start(ctx, opts) error`：启动 workflow runtime，初始化 workspace manager、multica client、runtime loop、任务并发信号量，并异步向 multica 注册 cs-cloud runtime。
  - `Stop(ctx) error`：停止 workflow runtime，取消运行中任务，并尝试注销 multica runtime。
  - `Health() error`：检查 runtime 健康状态。
  - `RunTaskAsync(payload) error`：同步预留任务后异步执行，避免 Gateway 30s 超时。
  - `AbortTask(taskID) error`：取消运行中任务；若任务尚未开始则写入 tombstone，防止后续 run 请求执行已取消任务。

#### `runtime.go`
- 长生命周期 runtime loop，负责：
  - 按 `HeartbeatInterval` 周期调用 `maintainRegistrations`，为每个 workspace 注册/维持 cs-cloud runtime 心跳（404 时重新注册）。
  - 按 `SyncInterval` 周期拉取 workspace 元数据并写入本地 JSON 缓存。
  - 预留 `GCInterval` 周期入口，当前 GC 逻辑为 stub，尚未实现。

#### `workspace.go`
- 工作区管理器：
  - 维护 `~/.costrict/cs-cloud/workflow/workspaces/` 目录。
  - 仓库检出、缓存、worktree 管理（已实现 `EnsureRepoReady` / `CreateWorktree`）。
  - GC 策略（按 workspace / task / 时间）：当前未实现。

#### `task.go`
- 任务执行器：
  - 解析云端下发的 task payload。
  - 准备执行环境（env、cwd、repo），当前 ProjectID → repoURL 解析为 no-op。
  - 调用本地 Agent CLI（直接 `exec.CommandContext`，默认 agent 由 multica 推送端硬编码为 `csc`）。
  - 任务结束后将 stdout/stderr 整体作为一条 text message 上报 multica；**不是实时 SSE/JSON 流式上报**。
  - 完成后由 `Driver.execute` 调用 `CompleteTask` / `FailTask`。

#### `client.go`
- multica 后端 REST 客户端：
  - 封装 `POST /api/daemon/register|heartbeat|deregister`。
  - 封装 `POST /api/daemon/tasks/:id/start|complete|fail|messages`。
  - 封装 workspace/project 查询 API（供 CLI 与 runtime sync 使用）。
  - 统一附加 `Authorization: Bearer <costrict_token>` 和 identity headers。
  - **当前未复用 cs-cloud 的 token 自动刷新逻辑**，仅静态读取 `~/.costrict/share/auth.json`；token 过期后需用户重新 `cs-cloud login`。
  - `/api/daemon/tasks/:id/usage` 与 `/session` 常量已定义，但 client 尚未实现对应方法。

### 4.2 `internal/localserver/workflow_handler.go` — Gateway 入口

新增路由（前缀 `/api/v1/workflow`，当前已实现）：

| 方法 | 路径 | 作用 |
|------|------|------|
| GET  | `/workflow/health` | driver 健康检查（driver 未注册返回 404，未运行返回 503） |
| POST | `/workflow/tasks/{id}/run` | 触发执行一个 workflow 任务（异步返回 `accepted`） |
| POST | `/workflow/tasks/{id}/abort` | 中止任务 |

以下路由在设计阶段列出，当前版本尚未实现：

- `POST /workflow/start`
- `POST /workflow/stop`
- `GET  /workflow/workspaces`
- `POST /workflow/workspaces/{id}/sync`
- `POST /workflow/gc`

### 4.3 `internal/cli/workflow.go` — 用户 CLI

当前已实现的 `cs-cloud workflow` 子命令：

```
cs-cloud workflow
└── workspace
    ├── list
    └── sync
```

其余命令（`issue`、`project`、`autopilot`、`repo`、`task`）已在 `internal/cli/workflow.go` 中预留入口，但实现为 stub，返回“not implemented yet”。

实现：
- 直接调用 multica 后端 API（复用 `internal/agent/workflow/client.go`）。
- 读取 `~/.costrict/share/auth.json` 获取 token。
- 输出格式为简单文本（未对齐原 cs-workflow 的 table/json 输出）。

### 4.4 `internal/workflow` — 共享层（新增）

存放跨 driver / handler / cli 共享的内容：

- `config.go`：workflow 配置结构（workspaces root、GC 参数、同步间隔、heartbeat 间隔、allowed agents）。
- `models.go`：workspace、issue、project、task 等 DTO；`TaskRunPayload` 包含可选 `Kind` 字段。
- `cache.go`：本地缓存读写，MVP 阶段使用 JSON 文件，当前仅实现 `workspaces.json`。
- `protocol.go`：multica 后端 API 路径常量、请求/响应类型；`usage`/`session` 端点已定义但 client 未调用。

### 4.5 `internal/localserver/server.go` — workflow driver 注入

workflow driver 不作为 `AgentManager` 的 persistent driver 管理，而是由 `Server` 直接持有：

- `Server` 新增 `workflow *workflow.Driver` 字段。
- 提供 `WithWorkflow(d *workflow.Driver) Option` 选项。
- `Server.Start()` 中显式调用 `s.workflow.Start()`；**启动失败直接返回 error，阻断 cs-cloud daemon 启动**。
- `Server.Shutdown()` 中显式调用 `s.workflow.Stop()` 后再停止其他组件。
- handler 直接通过 `s.workflow.RunTaskAsync(...)` / `s.workflow.AbortTask(...)` 调用，无需类型断言。

### 4.6 `internal/runtime/manager.go`

无需改动：workflow 不由 `AgentManager` 管理，保持其仅负责 per-conversation AI agent 的语义。

### 4.6 `internal/config` — 配置扩展

- 新增 `WorkflowConfig` 字段。
- 支持环境变量：
  - `CS_CLOUD_WORKFLOW_MULTICA_BASE_URL`
  - `CS_CLOUD_WORKFLOW_WORKSPACES_ROOT`
  - `CS_CLOUD_WORKFLOW_CACHE_DIR`
  - `CS_CLOUD_WORKFLOW_SYNC_INTERVAL`
  - `CS_CLOUD_WORKFLOW_GC_INTERVAL`
  - `CS_CLOUD_WORKFLOW_HEARTBEAT_INTERVAL`
  - `CS_CLOUD_WORKFLOW_AGENT_TIMEOUT`
  - `CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS`
  - `CS_CLOUD_WORKFLOW_ALLOWED_AGENTS`
- 默认值与派生规则：
  - `MulticaBaseURL`：默认空串；若 `CS_CLOUD_WORKFLOW_MULTICA_BASE_URL` 未设置且 `COSTRICT_BASE_URL` 非空，则派生为 `<COSTRICT_BASE_URL>/workflow-backend`；若最终仍为空，`config.Load()` 直接报错。
  - workspaces root：`~/.costrict/cs-cloud/workflow/workspaces`
  - cache dir：`~/.costrict/cs-cloud/workflow/cache`
  - sync interval：5m
  - GC interval：24h
  - heartbeat interval：15s
  - agent timeout：30m
  - max concurrent tasks：20
  - allowed agents：`["claude", "codex", "csc", "cs", "acp"]`

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
localserver.New(..., WithWorkflow(a.NewWorkflowDriver()), ...)
    │
    ▼
Server.Start()
    │
    ├──► workflow.Driver.Start()
    │       ├──► workspace manager 初始化 workspaces root / cache dir
    │       ├──► multica client 读取 ~/.costrict/share/auth.json token（当前无自动刷新）
    │       └──► 启动后台 goroutine：workspace 元数据同步、GC 占位、runtime 注册/心跳
    │
    └──► （启动失败直接返回 error，阻断 daemon 启动）
```

### 5.2 CoStrict 云端通过 Gateway 触发任务

```
CoStrict Web 控制台 / 调度服务
    │
    ▼
multica 服务端判断 runtime provider = cs-cloud
    │
    ▼
POST /device/:deviceID/proxy/api/v1/workflow/tasks/:id/run
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
workflow.Driver.RunTaskAsync(payload) ──► 同步预留成功后立刻返回 {"status":"accepted"}
    │
    ▼
task executor（在独立 goroutine 中运行）
```

### 5.3 任务执行与状态上报

```
task executor
    │
    ├──► 从 payload 解析 workspace_id、issue_id、project_id、agent、prompt 等
    │
    ├──► 解析 project_id → repoURL（当前为 no-op，实际 repoURL 为空）
    │
    ├──► workspaceManager.CreateWorktree(workspaceID, taskID, repoURL, "HEAD")
    │         │
    │         ├──► repoURL 非空 ──► EnsureRepoReady 命中/克隆 mirror ──► 创建 worktree
    │         └──► repoURL 为空 ──► 仅创建任务目录
    │
    ├──► 构建本地 Agent CLI 命令（当前 multica 推送端固定 agent="csc"）
    │         │
    │         ├──► 直接 exec.CommandContext，非 internal/agent 的 execenv wrapper
    │         └──► 注入 MULTICA_WORKSPACE_ID、MULTICA_TASK_ID、MULTICA_PROMPT、CS_CLOUD_WORKTREE
    │              （当前未注入 COSTRICT_TOKEN）
    │
    ├──► 启动 Agent 子进程
    │         │
    │         ├──► 收集完整 stdout/stderr
    │         └──► 任务结束后一次性调用 multicaClient.PostTaskMessages(taskID, output)
    │              （非实时 SSE/JSON 流式上报）
    │
    ├──► 任务完成/失败
    │         │
    │         ├──► multicaClient.CompleteTask(taskID, output)
    │         └──► 或 multicaClient.FailTask(taskID, error)
    │
    └──► usage 上报 ──► 当前未实现 multicaClient.PostTaskUsage
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
| 200 OK | 正常返回（`/run` 返回 `{"status":"accepted"}`，`/abort` 返回 `{"status":"aborted"}`） |
| 400 Bad Request | 返回错误 envelope，云端修正 payload |
| 404 Not Found | workflow driver 未注册到 `AgentManager`（`/start` 路由未实现，云端无法拉起） |
| 409 Conflict | 任务已在运行、并发槽满或已被 tombstone 拒绝，返回错误描述 |
| 500 Internal | 记录日志，返回错误，不自动重试（由云端侧决定是否重试） |
| 503 Unavailable | workflow driver 已注册但 `Health()` 未通过（未运行），建议云端延迟重试 |

### 6.3 multica 后端调用失败

| 场景 | 策略 |
|------|------|
| 401/403 token 失效 | **当前未实现自动 token 刷新**；调用失败，依赖用户重新执行 `cs-cloud login` 更新 `auth.json` |
| 404 task/workspace/runtime not found | 终止当前任务，清理本地状态，runtime 心跳 404 时重新注册 |
| 5xx / 网络超时 | 记录日志并失败；不实现指数退避重试 |
| 429 rate limit | 当前未特殊处理 |

### 6.4 任务执行失败

| 阶段 | 失败处理 |
|------|----------|
| repo 准备失败 | 上报 `task fail` 并附带错误详情；不保留不完整 worktree |
| Agent CLI 启动失败 | 上报 `task fail`；记录命令和环境用于排查 |
| Agent 运行中崩溃 | 捕获退出码，上报 `task fail`；stdout/stderr 已上报部分保留 |
| 任务被 abort | 通过 `context.CancelFunc` 立即终止进程；上报 `task fail` reason=aborted（当前未实现 SIGTERM → 5s → SIGKILL 的优雅期） |
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

- 当前 GC 逻辑为 stub，尚未实现周期性清理。
- 计划行为：GC 失败不阻塞任务执行；记录 error 日志，metrics 暴露 `workflow_gc_failures_total`；下次 GC 周期重试。

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
| `CS_CLOUD_WORKFLOW_MULTICA_BASE_URL` | `Workflow.MulticaBaseURL` | 空串（必填） | 显式设置时优先使用；未设置时从 `COSTRICT_BASE_URL` 派生为 `.../workflow-backend` |
| `CS_CLOUD_WORKFLOW_WORKSPACES_ROOT` | `Workflow.WorkspacesRoot` | `~/.costrict/cs-cloud/workflow/workspaces` | 工作区根目录 |
| `CS_CLOUD_WORKFLOW_CACHE_DIR` | `Workflow.CacheDir` | `~/.costrict/cs-cloud/workflow/cache` | 本地缓存目录 |
| `CS_CLOUD_WORKFLOW_SYNC_INTERVAL` | `Workflow.SyncInterval` | `5m` | workspace 元数据同步间隔 |
| `CS_CLOUD_WORKFLOW_GC_INTERVAL` | `Workflow.GCInterval` | `24h` | GC 执行间隔（GC 逻辑当前为 stub） |
| `CS_CLOUD_WORKFLOW_HEARTBEAT_INTERVAL` | `Workflow.HeartbeatInterval` | `15s` | multica runtime 心跳/注册维护间隔 |
| `CS_CLOUD_WORKFLOW_AGENT_TIMEOUT` | `Workflow.AgentTimeout` | `30m` | 单个任务超时 |
| `CS_CLOUD_WORKFLOW_MAX_CONCURRENT_TASKS` | `Workflow.MaxConcurrentTasks` | `20` | 最大并发任务数 |
| `CS_CLOUD_WORKFLOW_ALLOWED_AGENTS` | `Workflow.AllowedAgents` | `claude,codex,csc,cs,acp` | 允许执行的 agent CLI，逗号分隔 |

### 8.2 目录结构

```
~/.costrict/cs-cloud/workflow/
├── workspaces/           # 工作区目录（等价于原 ~/multica_workspaces）
│   ├── <workspace-id>/
│   │   ├── repos/        # repo cache
│   │   └── tasks/        # 任务级 worktree
└── cache/
    └── workspaces.json   # workspace 元数据缓存（当前唯一实现的缓存文件）
```

> `cache/issues.json`、`cache/state.json` 在设计文档中列出，当前版本尚未实现。  
> **日志**：workflow 相关日志统一写入 cs-cloud 日志（`~/.costrict/cs-cloud/logs/`），不单独维护 workflow.log。未来若流量过大再考虑分离。

---

## 9. 风险与待确认事项

### 9.1 高风险

1. **CoStrict token 能否被 multica 后端识别？**
   当前实现假设 CoStrict OAuth token 可以直接用于 multica 后端，且 workflow client **未实现 token 自动刷新**。token 过期后所有 multica 调用都会 401，需要用户重新 `cs-cloud login`。

2. **multica 后端任务推送到 cs-cloud 的链路**
   multica 服务端已实现 server-side push：任务入队后通过 Gateway `POST /device/:deviceID/proxy/api/v1/workflow/tasks/:id/run` 推送到设备。该链路依赖 `MULTICA_CLOUD_FLEET_URL` 与 `COSTRICT_INTERNAL_SECRET` 配置正确。

3. **任务执行与 Gateway 30s 超时边界**
   `/run` handler 通过 `RunTaskAsync` 同步预留任务后立刻返回 `accepted`，实际 agent 运行在后台 goroutine 中。需确保预留阶段逻辑足够轻量，不会 itself 超过 Gateway 超时。

### 9.2 中风险

1. **仓库缓存/GC 逻辑未完整实现**
   repo mirror / worktree 创建已实现，但 ProjectID → repoURL 解析为 no-op，GC 逻辑为 stub。长期运行可能积累大量 task worktree。

2. **CLI 命令大量为 stub**
   除 `cs-cloud workflow workspace list/sync` 外，`issue`、`project`、`autopilot`、`repo`、`task` 子命令均未实现。

3. **跨平台兼容性**
   workspace、repo、Agent CLI 路径在不同 OS 上可能有差异，需要充分测试 Windows 行为。

### 9.3 待实现/待确认

1. 实现 `internal/agent/workflow/client.go` 的 `PostTaskUsage` / `PostTaskSession`。
2. 任务执行时向 agent env 注入 `COSTRICT_TOKEN`（若 `csc` 执行期间需要调 multica API）。
3. 补齐 abort 的 SIGTERM → 5s → SIGKILL 优雅期。
4. 实现 workspace GC 策略与 `/workflow/gc` 路由/CLI。
5. 实现 ProjectID → repoURL 查询，补全 repo checkout 链路。

---

## 10. 下一步

本设计文档通过评审后，进入 `writing-plans` 阶段，输出详细实施计划，包括：

1. 第一阶段：基础设施（目录结构、配置、共享类型、multica client）。
2. 第二阶段：runtime manager 扩展 + workflow driver 骨架。
3. 第三阶段：workspace/repo 管理 + GC。
4. 第四阶段：任务执行 + 状态上报。
5. 第五阶段：localserver 路由 + CLI 子命令。
6. 第六阶段：测试、文档、端到端验证。
