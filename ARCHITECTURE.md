# cs-cloud 架构文档

> 本文描述 cs-cloud 的整体架构、模块边界、关键数据流与扩展点。
> 配合 [`README.md`](README.md) 一起阅读。

---

## 整体架构

cs-cloud 是一个**单进程多职责**的本地代理，运行在用户设备上。它需要同时承担以下角色：

1. **守护进程**：常驻后台，提供 start/stop/restart 生命周期
2. **本地 HTTP Control Plane**：暴露 REST API 给云端隧道调用
3. **WebSocket 隧道客户端**：与 CoStrict 网关保持长连接
4. **Agent Runtime 管理器**：调度本地 AI Agent 子进程
5. **终端会话管理器**：管理 PTY 子进程
6. **自动升级器**：检查新版本并热替换 binary

### 顶层架构图

```
┌─────────────────────────────────────────────────────────────┐
│                    CoStrict Cloud (云端)                      │
│  ┌────────────┐    ┌─────────────┐    ┌──────────────────┐   │
│  │   Server   │◄──►│   Gateway   │◄──►│  costrict-web    │   │
│  │  (业务API) │    │ (设备隧道)  │    │  (Web 控制台)    │   │
│  └────────────┘    └──────┬──────┘    └──────────────────┘   │
└─────────────────────────────┼───────────────────────────────┘
                              │ WebSocket + yamux
                              │ (单连接多路复用)
┌─────────────────────────────┼───────────────────────────────┐
│                   用户设备（本地）                            │
│  ┌──────────────────────────▼──────────────────────────────┐ │
│  │                    cs-cloud daemon                       │ │
│  │                                                          │ │
│  │  ┌──────────┐   ┌──────────────┐   ┌──────────────┐    │ │
│  │  │  Tunnel  │◄─►│  LocalServer │◄─►│ AgentRuntime │    │ │
│  │  │ (隧道)   │   │ (HTTP API)   │   │ (agent 管理) │    │ │
│  │  └──────────┘   └──────────────┘   └──────┬───────┘    │ │
│  │                        ▲                  │             │ │
│  │                        │                  ▼             │ │
│  │  ┌──────────┐   ┌──────┴─────┐   ┌──────────────┐     │ │
│  │  │ Updater  │   │  Terminal   │   │   csc / cs   │     │ │
│  │  │ (升级)   │   │  (PTY)      │   │  (子进程)    │     │ │
│  │  └──────────┘   └────────────┘   └──────────────┘     │ │
│  └──────────────────────────────────────────────────────────┘ │
│                                                                │
│  ~/.costrict/share/auth.json  (与 opencode 共享)               │
│  ~/.costrict/cs-cloud/logs/   (按天滚动)                       │
└────────────────────────────────────────────────────────────────┘
```

---

## 模块清单与边界

代码组织遵循"职责单一、依赖单向"原则。下面按依赖层次（从底层到顶层）展开。

### 平台层（无外部依赖）

| 模块 | 路径 | 职责 |
|------|------|------|
| `platform` | `internal/platform` | OS 差异抽象（路径、信号、进程组、null 设备） |
| `logger` | `internal/logger` | zap + lumberjack 封装，按天滚动 |
| `version` | `internal/version` | 构建时注入的版本信息 |
| `model` | `internal/model` | 领域模型与 DTO |

### 配置层

| 模块 | 路径 | 职责 |
|------|------|------|
| `config` | `internal/config` | 配置加载、auth.json/device.json 读写、环境变量解析 |
| `provider` | `internal/provider` | OAuth 登录、Token 刷新、凭证管理 |

### 设备与连接层

| 模块 | 路径 | 职责 |
|------|------|------|
| `device` | `internal/device` | 设备注册、心跳、网关验证、device_id（MAC 地址算法） |
| `tunnel` | `internal/tunnel` | WebSocket 隧道、yamux 多路复用、断线重连 |
| `cloud` | `internal/cloud` | 与云端 API 的客户端封装 |

### 运行时层

| 模块 | 路径 | 职责 |
|------|------|------|
| `runtime` | `internal/runtime` | Agent Runtime 抽象层（driver + event bus + session store） |
| `agent` | `internal/agent` | Agent 子进程管理、命令派发、proxy helpers |
| `terminal` | `internal/terminal` | PTY 管理、二进制 WS、Windows ConPTY / Unix pty |
| `filewatcher` | `internal/filewatcher` | 文件变更监听 |
| `gitwatcher` | `internal/gitwatcher` | Git 事件监听（push/fetch/branch） |

### 服务层

| 模块 | 路径 | 职责 |
|------|------|------|
| `localserver` | `internal/localserver` | 本地 HTTP Control Plane（路由、handler、CORS、Swagger） |
| `updater` | `internal/updater` | 版本检查、下载、热替换（Windows cmd.exe helper） |

### 入口层

| 模块 | 路径 | 职责 |
|------|------|------|
| `cli` | `internal/cli` | Cobra 命令（login/start/stop/restart/status/doctor/...） |
| `app` | `internal/app` | 应用装配、依赖注入、生命周期管理 |
| `cmd/cs-cloud` | `cmd/cs-cloud/main.go` | 程序入口 |

### 依赖方向

```
cmd/cs-cloud
    │
    ▼
  app ◄──── 装配所有模块
    │
    ▼
  cli  ──── 调用 ───►  app / provider / localserver / tunnel / updater
                          │
                          ▼
                    runtime / terminal / device / cloud
                          │
                          ▼
                    agent / filewatcher / gitwatcher
                          │
                          ▼
                    config / model / logger / platform / version
```

**原则**：上层可以依赖下层，下层不能依赖上层；同层之间通过 `app` 注入，避免直接耦合。

---

## 关键数据流

### 1. 启动流程（`cs-cloud start`）

```
cli.start
  │
  ├─► 检查已在运行（PID 文件 + 健康检查）
  │
  ├─► 加载 config（auth.json + 环境变量）
  │     │
  │     └─► 若 auth 缺失 → 触发 login 浏览器流程
  │
  ├─► device.register（POST /api/devices/register）
  │     │
  │     ├─► 若 401 → 清除 device.json → 重新 register
  │     └─► 若 token 过期 → provider.refresh_token
  │
  ├─► device.gateway-assign（验证 device_token，分配 gateway）
  │
  ├─► tunnel.connect（WebSocket 握手，建立 yamux 多路复用）
  │
  ├─► localserver.start（HTTP Control Plane，绑定 127.0.0.1）
  │
  └─► daemon.detach（fork 自身到后台，写 PID 文件）
```

### 2. 云端调度本地 Agent 的完整链路

```
Web 控制台用户点击"运行 Agent"
    │
    ▼
costrict-web Server ──HTTP──► Gateway ──隧道──► cs-cloud
                                                    │
                                                    ▼
                                          localserver.command_dispatcher
                                                    │
                                                    ▼
                                          runtime.manager 启动 agent 子进程
                                                    │
                          ┌─────────────────────────┼─────────────────────────┐
                          ▼                         ▼                         ▼
                    csc / cs 子进程            terminal PTY            file/git watcher
                          │                         │                         │
                          ▼                         ▼                         ▼
                    SSE 事件流                PTY 输出流              事件回调
                          │                         │                         │
                          └─────────────┬───────────┴─────────────────────────┘
                                        ▼
                              eventbus 聚合事件
                                        │
                                        ▼
                              tunnel 流式回传到 gateway
                                        │
                                        ▼
                              Web 控制台实时显示
```

### 3. Token 刷新三层策略

```
判断 access_token 是否过期
    │
    ├─► 第 1 层：检查 expiry_date（30 分钟缓冲）
    │           └─► 若有 expiry_date 且未过期 → 直接使用
    │
    ├─► 第 2 层：解析 refresh_token JWT 的 exp
    │           └─► 若 refresh_token 仍有效 → 用它换取新 access_token
    │
    └─► 第 3 层：解析 access_token JWT 的 exp（30 分钟缓冲）
                └─► 若仍未过期 → 使用，并触发后台异步刷新
                    否则 → 强制重新登录
```

### 4. 自动升级流程

```
启动时 / 手动 update
    │
    ▼
updater.check_latest（Gitee Release API，含失败重试）
    │
    ├─► 无新版本 → 跳过
    │
    └─► 有新版本 →
          ├─► 下载 binary 到临时文件（校验 SHA256）
          │
          ├─► Linux/macOS：原子替换 + restart
          │
          └─► Windows：
                ├─► 写 cmd.exe helper 脚本
                ├─► 等待主进程退出
                ├─► helper 替换 binary
                └─► helper 重启 daemon
```

---

## 扩展点

### 接入新的 Agent CLI

`internal/runtime/registry.go` 是 agent driver 的注册中心。要接入新 agent：

1. 实现 `driver.Driver` 接口（在 `internal/runtime/capabilities.go` 定义）
2. 在 `internal/agent/{vendor}/` 下新增实现包
3. 在 registry 中注册

当前已支持：`csc`、`cs`、`acp`（兼容协议）。

### 接入新的常驻子系统（以 workflow 为例）

当前 cs-cloud 的常驻子系统由 `internal/localserver/server.go` 直接持有并在 `Start` / `Shutdown` 中显式启停（与 `filewatcher`、`gitwatcher` 等保持一致）。新增一个常驻子系统：

1. 在 `internal/localserver/server.go` 的 `Server` 结构体中新增字段（如 `workflow *workflowagent.Driver`）。
2. 提供 `WithXxx(...)` Option，在 `localserver.New(...)` 调用处注入（如 `internal/cli/serve.go`、`internal/cli/daemon.go`）。
3. 在 `Server.Start()` 中显式调用其 `Start()` 方法；若为业务关键组件，启动失败应直接返回 error 阻断 daemon 启动。
4. 在 `Server.Shutdown()` 中显式调用其 `Stop()` 方法。
5. handler 直接通过 `Server` 字段调用子系统能力，无需经过 `AgentManager`。

当前已接入：`workflow`（cs-workflow 迁移，调用 multica 后端并暴露 `/api/v1/workflow/*` 路由）。当前实现的路由为：

- `GET  /api/v1/workflow/health` — driver 健康检查
- `POST /api/v1/workflow/tasks/{id}/run` — 接收云端推送的任务（异步返回 `accepted`）
- `POST /api/v1/workflow/tasks/{id}/abort` — 中止任务

handler 实现位于 `internal/localserver/workflow_handler.go`，路由注册在 `internal/localserver/server.go`。

### 接入新的本地 API

`internal/localserver/server.go` 集中注册路由，handler 可放在 `internal/localserver/` 下（如 `internal/localserver/workflow_handler.go`）：

1. 新增 handler 方法
2. 在 `internal/localserver/server.go` 的 `New()` 中注册路由 + 必要选项（如 `WithWorkflowDriver`）
3. 如需 Swagger，补充 `openapi.json` 与 `/docs` 路由（当前 workflow 路由为内部 Gateway 使用，未进 Swagger）

### 接入新平台

`internal/platform/` 使用 `*_unix.go` / `*_windows.go` 文件后缀做平台隔离。新增平台只需补对应文件，无需改业务代码。

---

## 关键设计决策

### 1. 为什么用 yamux 多路复用而不是多 WebSocket？

- 单 WebSocket 连接对反向代理 / 防火墙穿透更友好
- yamux 在应用层实现多路复用，可以承载并发 RPC 和流式数据
- keepalive 在 yamux 层统一处理，比维护多个 WS 连接简单

### 2. 为什么 device_id 基于 MAC 地址而不是 UUID？

- 虚拟机克隆后 UUID 会变，导致设备身份失效
- MAC 地址在克隆后保持稳定（除非显式重新生成）
- 配套 legacy_device_id 字段兼容旧 UUID

### 3. 为什么 localserver 只监听 127.0.0.1？

- 本地 Control Plane 只应被 tunnel 访问，不暴露到网络
- 避免同网段其他设备未授权访问
- 想外部访问需要显式配置

### 4. 为什么用 Cobra + lipgloss 做而不是简单 CLI？

- 跨平台信号处理（daemon_signal_unix.go / _windows.go）
- 跨平台 daemon detach
- 终端 UI 美观（lipgloss 静态颜色定义，自适应亮色背景）
- 子命令扩展性强

---

## 测试策略

| 类型 | 位置 | 覆盖范围 |
|------|------|---------|
| 单元测试 | `*_test.go` | 命令分发、find_files、init_status、swagger、version、kill_windows、connect、manager、proxy_helpers |
| HTTP 集成测试 | `*_http_test.go` | command_handler 的端到端 |
| 跨平台验证 | 手动 | Linux + Windows 各跑一次 `start → doctor → stop` |

**当前不足**：runtime manager 之外的 agent 子进程链路缺少端到端测试，是下半年补齐重点。

---

## 性能与资源占用

- **启动时间**：< 2 秒（含 OAuth 检查与设备注册）
- **内存占用**：常驻 ~30 MB（不含 agent 子进程）
- **CPU 占用**：空闲 ~0%，活跃时取决于 agent 数量
- **日志大小**：按天滚动，单文件 ~5 MB，保留最近 10 个

---

## 与其他仓库的关系

| 仓库 | 关系 |
|------|------|
| **opencode** | 共享 `~/.costrict/share/auth.json`；cs-cloud 调度 opencode 或兼容 agent |
| **costrict-web** | 云端调度方，通过 Gateway 与 cs-cloud 通信 |
| **csc** | 本地适配器，cs-cloud 的 agent CLI 之一 |

详细接口契约见 `docs/api-data-structures.md` 和 `docs/runtime-control-api.md`。

---

## 变更日志

架构层面的重要变更请在此处追加：

- **2026-06-30**：新增 doctor 命令的本地/云端在线状态检测
- **2026-06-29**：file browser 支持列出文件系统根
- **2026-06-22**：runtime 增加 `--agent-path` flag，自动派生命令
- **2026-06-17**：agent 增加版本检测、restart-agent 命令、SSE 重构
- **2026-05-29**：notification forwarder 加入，支持 permission batch 与 question buffer
- **2026-05-13**：默认 agent runtime 改为 csc
