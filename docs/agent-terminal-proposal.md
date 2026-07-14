# Agent 驱动的 Terminal 资源统一提案

> 让 AI Agent (csc) 通过 builtin tool 创建/操作 terminal，与用户从 UI 直接操作的 terminal 共享同一份 PTY 资源；cs-cloud 暴露标准化能力接口供云端/web 消费。
>
> 配合 [`cloud-terminal.md`](./cloud-terminal.md)、[`runtime-control-api.md`](./runtime-control-api.md) 一起阅读。

---

## 1. 背景与目标

### 1.1 现状

- [`cloud-terminal.md`](./cloud-terminal.md) 已经设计了 cs-cloud 直接管理 PTY 的完整方案：创建/删除/resize/stream/input/input-ws，由用户从 web UI 触发。
- [`runtime-control-api.md`](./runtime-control-api.md) 也定义了 `/runtime/terminals/*` RESTful 接口。
- csc 已有 `MonitorTool`（feature flag `MONITOR_TOOL` 控制）通过 `spawnShellTask` 后台执行长命令，但输出走 outputFile，AI 用 Read 读，**没有实时面板能力**。
- csc 缺少 stdin 介入工具（不能在 task 运行中给 stdin 写数据）。

### 1.2 目标

| 需求 | 现状 | 目标 |
|------|------|------|
| AI 运行长期命令 | MonitorTool（无面板） | 复用 cs-cloud PTY 资源 + UI 实时面板 |
| AI 查看运行结果 | Read 读 outputFile | SSE 实时输出流 + UI 面板 |
| AI stdin 介入 | ❌ 缺失 | 新增 SendInputTool |
| 用户 UI 操作 | 已有（cloud-terminal） | 与 AI 共享同一资源 |
| 跨 turn 寻址 | task 在 csc 内部 | cs-cloud terminal registry first-class 资源 |

### 1.3 与 cloud-terminal.md 的关系

**完全复用** cloud-terminal.md 定义的 PTY 管理基础设施（`internal/terminal/manager.go` 等）。本提案的核心扩展是：

1. **多触发器**：在用户触发之外，新增 AI tool 触发路径
2. **first-class 资源化**：terminal 上升到 cs-cloud 资源层，独立于 message 生命周期
3. **AI/UI 共享**：同一 terminalID 可被 AI tool 和 UI 同时操作
4. **审批分轨**：AI 触发走 tool permission，用户触发走身份验证 + 审计

---

## 2. 总体架构

```
┌─ 触发层 ─────────────────────────────────────────────────────┐
│  ┌──────────────────┐        ┌──────────────────────────┐    │
│  │ AI (csc builtin) │        │ User (web UI)            │    │
│  │ MonitorTool      │        │ POST /api/v1/terminal    │    │
│  │ SendInputTool    │        │ POST .../input           │    │
│  └────────┬─────────┘        └───────────┬──────────────┘    │
└───────────┼──────────────────────────────┼───────────────────┘
            │ tool RPC                      │ REST/WS
            ▼                               ▼
┌─ csc ──────────────────────────────────┐ ┌─ cs-cloud localserver ────┐
│ LocalShellTask (task registry + pty)   │ │ /api/v1/terminal/*        │
│   ↓ emit                               │ │ (cloud-terminal.md 已定义)│
│ 事件标准化层（csc 内闭环）             │ └───────────┬───────────────┘
│   - base64 编码 / 时间戳统一            │             │               │
│   - state/stream 枚举校验               │             │               │
│   - 序号 seq 分配                       │             │               │
│   ↓ 符合 §5 契约的事件                  │             │               │
│ csc SSE upstream                       │             │               │
└─────────────┬──────────────────────────┘             │               │
              │ SSE（已是标准契约）                   │ 同进程         │
              ▼                                        ▼               │
┌─ cs-cloud adapter_sse.go ────────────────────────────────────────┐
│ - 识别 terminal.* 事件（按 type 路由）                            │
│ - **透传**：不做字段转换，原样转发到 tunnel                       │
│ - 同步到 terminal registry（登记 + fan-out）                      │
└─────────────┬────────────────────────────────────────────────────┘
              │                                                 │
              ▼                                                 │
┌─ cs-cloud TerminalRegistry (新增) ─────────────────────────────┐
│ - sessionID → terminalID → owner(csc/upstream) 映射             │
│ - 多订阅者 fan-out                                              │
│ - AI/UI 创建来源标记                                            │
│ - idle/exit 清理                                                │
└──────┬──────────────────────────────────────────────────────────┘
       │ tunnel (yamux)
       ▼
┌─ cloud / costrict-web ────────────────────────────────────────┐
│ - DeviceProxyHandler 透传 SSE/WS (已有)                       │
│ - web UI: xterm.js / ghostty-web 订阅 terminal.* + REST 控制  │
└───────────────────────────────────────────────────────────────┘
```

**核心原则**：terminal 是 cs-cloud 维护的 first-class 资源，AI tool 和 UI 是平等的两种触发器。

---

## 3. csc 侧改动

### 3.1 工具列表

| 工具 | 触发方 | 用途 | 备注 |
|------|--------|------|------|
| `MonitorTool` (已有) | AI | 启动长跑命令，返回 terminalID | 改造：emit `terminal.*` 事件 |
| `SendInputTool` (新增) | AI | 向运行中 terminal 写 stdin | 走 permission.asked |
| `TerminalListTool` (新增) | AI | 列出当前 session 活跃 terminal | 选读工具 |
| `TerminalKillTool` (新增) | AI | 终止指定 terminal | 走 permission.asked |

### 3.2 MonitorTool 改造

保持现有接口（输入 `command` / `description`），但内部行为变化：

**之前**：`spawnShellTask` → 返回 `{taskId, outputFile}`，AI 轮询 Read。

**之后**：
1. `spawnShellTask` 仍然返回 `{taskId, outputFile}`（向后兼容）
2. 额外通过 csc SSE 通道 emit：
   - `terminal.created`（携带 taskId、command、cwd、rows/cols）
   - `terminal.output`（每个 stdout/stderr chunk）
   - `terminal.state`（running/idle/exited）
   - `terminal.exited`（exitCode/signal）
3. cs-cloud 适配层识别这些事件，登记到 TerminalRegistry，转发上行

**好处**：
- AI 仍可用 Read 读 outputFile（不变）
- UI 同时收到事件流，渲染实时面板
- 后续 SendInputTool 通过同一 taskId 介入

### 3.3 SendInputTool 定义

```typescript
{
  name: "send_input",
  description: "Send input to a running terminal task (stdin)",
  input: {
    taskId: string,        // MonitorTool 返回的 taskId
    data: string,          // 文本内容（自动 UTF-8 编码）
    ctrl?: boolean,        // true 时 data 解释为控制字符名：c/d/z/...
    encoding?: "text" | "base64"
  },
  // 走 permission.asked（stdin = 任意执行）
}
```

**实现**：调 `LocalShellTask.writeStdin(taskId, encoded)`，并 emit `terminal.input` 事件（审计用，不携带 raw data）。

### 3.4 csc SSE 协议扩展

在现有 `session.message` / `session.permission_replied` 之外新增 `terminal.*` 系列。所有事件共享字段：

```jsonc
{
  "type": "terminal.<subtype>",
  "sessionID": "...",
  "terminalID": "...",     // csc 内部 taskId（直接作为 terminalID 暴露）
  "timestamp": 1234567890  // 毫秒，emit 时由 csc 生成
}
```

**标准化在 csc 内闭环原则**：

csc 在 emit 之前必须完成所有字段处理，包括：

- **base64 编码**：所有 `data` 字段在 csc 端就完成 base64（防 SSE 帧注入、二进制安全）
- **时间戳**：csc emit 时立即打 `timestamp`（毫秒级 Unix 时间）
- **枚举校验**：`state`、`stream` 枚举值在 csc 端就规范化（不允许下游再映射）
- **序号分配**：单 terminal 内 `seq` 由 csc 单调递增分配
- **terminalID 暴露**：csc 内部 taskId 直接作为对外 terminalID 使用，避免 cs-cloud 再做 ID 映射

**cs-cloud 只做透传 + 资源登记**，不做字段转换。这样：

- 协议契约在 csc 一侧定义清晰，下游（cs-cloud / cloud / web）拿到的就是最终形态
- 字段变更只需要改 csc 一处，不需要 cs-cloud 配合发版
- cs-cloud adapter 退化为薄薄的事件路由层，降低维护成本

子类型见 §5（事件 schema 是 csc emit 时必须满足的契约，不是 cs-cloud 转换后的结果）。

---

## 4. cs-cloud 侧改动

### 4.1 TerminalRegistry（新增）

位于 `internal/terminal/registry.go`，与 `cloud-terminal.md` 的 `TerminalManager` 协作：

```go
type TerminalRegistry struct {
    mu       sync.RWMutex
    items    map[string]*TerminalEntry  // key = terminalID
    session  map[string]map[string]bool // sessionID → terminalID set
}

type TerminalEntry struct {
    ID         string
    SessionID  string
    Owner      string  // "csc" / "user" / "cs"
    Upstream   string  // 来源（csc 实例 / REST 请求）
    CreatedAt  time.Time
    State      string  // running / idle / exited / killed
    Command    string
    Cwd        string
    // 订阅 fan-out
    subscribers map[string]chan Event
}
```

**职责**：
- 跨触发器统一登记（无论 csc emit 还是 REST POST 创建）
- 多订阅者 fan-out（同一 terminal 可能被多个 web tab 订阅）
- 重连后状态恢复（基于 registry 重建 SSE 流）
- idle 清理（已有 cloud-terminal 设计的 30 分钟超时）

### 4.2 adapter_sse.go 扩展

cs-cloud adapter 作为**透传 + 资源登记**层，**不做字段转换**。事件契约由 csc 侧保证（见 §3.4）。

在 `adaptEventMap()` 中新增 case：

```go
case "terminal.created":
    // 直接透传 + 登记
    registry.RegisterTerminal(sessionID, payload)
    return []sseFrame{frame("terminal.created", payload)}

case "terminal.output":
    // 直接透传 + fan-out
    registry.Fanout(sessionID, payload)
    return []sseFrame{frame("terminal.output", payload)}

case "terminal.exited":
    // 直接透传 + 状态更新
    registry.UpdateState(sessionID, payload.TerminalID, "exited")
    return []sseFrame{frame("terminal.exited", payload)}
// ...
```

**adapter 仅做的事**：

- 按 `type` 路由事件
- 提取 `terminalID` / `sessionID` 用于 registry 操作
- 同一事件 fan-out 给多个订阅者（每个订阅者独立 SSE 队列）
- 原样转发到 tunnel 上行

**adapter 不做的事**：

- ❌ base64 编解码（csc 已完成）
- ❌ timestamp 重新生成（csc 已完成）
- ❌ 枚举值映射（csc 已规范化）
- ❌ ID 重命名（csc taskId 即对外 terminalID）
- ❌ 字段名转换（命名风格由 csc 一侧统一）

如果未来发现 csc emit 的事件不符合下游需求，**应该改 csc 而不是在 adapter 打补丁**。

### 4.3 localserver 扩展

在 `cloud-terminal.md` 已有路由基础上，新增：

```
GET    /api/v1/terminal                      列出所有 terminal（含 AI 创建的）
GET    /api/v1/terminal/{id}/output?offset=  分页回看历史输出
POST   /api/v1/terminal/{id}/signal          发信号（SIGINT/SIGTERM）
```

`POST /api/v1/terminal/{id}/input` 端点扩展支持来源标记：

```json
{
  "data": "base64...",
  "source": "user" | "ai",   // 用于审计
  "ctrl": false               // 可选，控制字符
}
```

---

## 5. 标准化事件 Schema（csc emit 时的契约）

> **以下 schema 是 csc 在 emit `terminal.*` 事件时必须满足的契约**。cs-cloud 与下游消费者按此契约解析，不再做字段转换。任何字段调整都意味着 csc 单边的版本演进。

```jsonc
{
  "type": "terminal.created",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",      // cs-cloud 维护的全局 ID
  "owner": "ai" | "user",
  "command": "npm run dev",
  "cwd": "/proj",
  "rows": 24,
  "cols": 80,
  "createdAt": 1234567890
}
```

### 5.2 terminal.output

```jsonc
{
  "type": "terminal.output",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",
  "stream": "stdout" | "stderr" | "system",
  "data": "base64...",          // 单帧 ≤ 32KB，超出分块
  "seq": 42,                    // 单调递增，UI 端重排
  "timestamp": 1234567890
}
```

### 5.3 terminal.state

```jsonc
{
  "type": "terminal.state",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",
  "state": "running" | "idle" | "exited" | "killed",
  "timestamp": 1234567890
}
```

### 5.4 terminal.exited

```jsonc
{
  "type": "terminal.exited",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",
  "exitCode": 0,
  "signal": null | "SIGINT",
  "timestamp": 1234567890
}
```

### 5.5 terminal.closed

资源回收通知（保留窗口结束后清理）：

```jsonc
{
  "type": "terminal.closed",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",
  "reason": "timeout" | "manual" | "session_end"
}
```

### 5.6 terminal.input（审计事件）

由 SendInputTool 或用户 input 触发，**不携带 raw data**：

```jsonc
{
  "type": "terminal.input",
  "sessionID": "sess_abc",
  "terminalID": "trm_xyz",
  "source": "ai" | "user",
  "bytes": 7,                   // 写入字节数
  "ctrl": false,
  "timestamp": 1234567890
}
```

---

## 6. 审批模型分轨

| 触发者 | 通道 | 审批策略 | 审计 |
|--------|------|----------|------|
| AI 调 MonitorTool | tool → permission.asked | 强制审批 command | always |
| AI 调 SendInputTool | tool → permission.asked | 强制审批 stdin 内容 | always |
| AI 调 TerminalKillTool | tool → permission.asked | 强制审批 | always |
| 用户 POST /terminal (创建) | REST + 身份验证 | 已有 cloud-terminal 流程 | always |
| 用户 POST /terminal/{id}/input | REST + 身份验证 | 默认放行 + 高危检测 | always |

### 6.1 高危检测（用户路径）

cs-cloud 在 input handler 中扫描 base64 解码后的字符串，匹配危险模式：

- `rm -rf /` / `rm -rf ~`
- fork bomb `:(){:|:&};:`
- `dd if=... of=/dev/sd*`
- `mkfs` / `shutdown` / `reboot`

匹配到时返回 `403` + 推送 `terminal.input_blocked` 事件，UI 弹二次确认。

### 6.2 AI 路径细粒度审批

`SendInputTool` 走 csc 的 `checkPermissions`，建议策略：

- 默认：每次调用都 `permission.asked`（最安全）
- 增强：检测明显危险输入（`rm -rf` 等）才审批，普通交互输入（`y\n`、`Y\n`、空行）放行（用户体验更好）

---

## 7. AI / UI 共享与冲突处理

### 7.1 共享模型

```
terminalID = "trm_xyz"
   ├─ AI: 通过 SendInputTool 写入
   ├─ AI: 订阅 terminal.output 知道进程状态
   ├─ UI: 通过 REST input 写入
   └─ UI: 订阅 SSE 实时渲染
```

四条路径**共用同一个 pty stdin / stdout**，互斥由 cs-cloud 保证。

### 7.2 写入串行化

cs-cloud TerminalRegistry 维护 terminal 级互斥锁：

```go
func (r *TerminalRegistry) WriteInput(id string, data []byte, source string) error {
    entry := r.get(id)
    entry.writeMu.Lock()
    defer entry.writeMu.Unlock()
    // 写入 pty stdin
    // emit terminal.input (source, len(data))
}
```

保证 AI 和用户同时写时按到达顺序串行执行。

### 7.3 inputLock（可选增强）

UI 提供"接管"按钮：

- 按下后，registry 标记 terminal 为 `user_locked`
- AI 调 SendInputTool 时收到 `409 Conflict` + 提示"用户正在操作"
- 释放后 AI 恢复访问

避免 AI 抢键。

### 7.4 进程中断感知

AI 调 MonitorTool 等 `npm test` 跑完时，用户中途 Ctrl+C：

- pty 收到 `\x03` → 进程退出 → emit `terminal.exited {signal: "SIGINT"}`
- AI 订阅 output 看到 exit 事件，自然知道被中断
- 不需要额外通知通道

---

## 8. 背压与缓冲

| 位置 | 缓冲策略 | 溢出处理 |
|------|----------|----------|
| csc LocalShellTask | ring buffer 1MB/terminal | drop oldest + 推送 `stream:"system"` 提示 |
| cs-cloud SSE 队列 | 每订阅者 256KB | drop oldest + 推送截断提示 |
| cs-cloud input 队列 | 无（同步串行） | 直接拒绝（429） |
| web xterm scrollback | 10000 行 | 滚动丢弃 |

**SSE 帧上限**：单帧 ≤ 32KB（含 base64），超出由 csc 端分块。

---

## 9. 安全考虑

### 9.1 stdin = 任意执行

任何写入 stdin 的能力都等同于命令执行。缓解：

- AI 路径强制走 `permission.asked`
- 用户路径加高危模式检测
- 所有 input 写入审计记录（who/when/what/sessionID/terminalID）

### 9.2 多租户隔离

costrict-web 已有 `DeviceProxyHandler` 做用户身份 + 设备归属校验。REST 路径自动受益。

csc SSE 路径：cs-cloud adapter 校验 sessionID 归属（已有逻辑）。

### 9.3 资源耗尽

- 单 session terminal 数量上限（默认 20）
- 单 terminal 输出缓冲上限（1MB）
- idle 超时清理（30 分钟无 input）
- 进程退出后保留窗口（5 分钟）再清理 registry 条目

---

## 10. 实施分阶段

| 阶段 | 内容 | 依赖 | 工作量 |
|------|------|------|--------|
| 1 | 接口 schema 定义 + 协议文档更新（csc emit 契约 + REST 接口） | 无 | 小 |
| 2 | cs-cloud TerminalRegistry 实现 | cloud-terminal.md 的 PTY 基础设施 | 中 |
| 3 | adapter_sse.go 识别 + **透传** terminal.*（无字段转换） | 阶段 2 | 小 |
| 4 | csc emit `terminal.*` 事件 + **标准化在 csc 内闭环**（base64/timestamp/枚举/seq/ID） | 阶段 1 | 中 |
| 5 | MonitorTool 改造（emit + 字段对齐） | 阶段 4 | 小 |
| 6 | SendInputTool + permission 流 | 阶段 5 | 中 |
| 7 | localserver 扩展端点（output 分页/signal/input source 标记） | 阶段 2 | 中 |
| 8 | 高危检测 + inputLock + 审计日志 | 阶段 7 | 中 |
| 9 | web UI xterm.js / ghostty-web 集成 + optimistic echo | 阶段 3 | 大 |
| 10 | 端到端联调（AI 触发 + UI 介入 + 重连恢复） | 全部 | 中 |

### 10.1 关键里程碑

- **M1（阶段 1-3）**：cs-cloud 内部链路打通，可用 mock csc 事件验证 registry/SSE 转发
- **M2（阶段 4-6）**：AI 路径端到端可用，AI 能创建 terminal、写入 stdin、看到 output
- **M3（阶段 7-8）**：UI 介入可用，与 AI 共享资源
- **M4（阶段 9-10）**：完整产品体验

---

## 11. 风险与权衡

| 风险 | 影响 | 缓解 |
|------|------|------|
| csc `feature('MONITOR_TOOL')` 是编译期 flag | cs-cloud 拉起的 csc build 必须包含该 flag | 修改 csc build.ts，单独产出含 MONITOR_TOOL 的 build |
| prompt cache 失效 | tool 列表变化影响所有用户 | 工具顺序固定，与 Statsig system prompt 同步 |
| Windows PTY 兼容 | `creack/pty` Windows 路径需 ConPTY | cloud-terminal.md 已识别此风险 |
| SSE 跨多层代理延迟 | UI 打字延迟感 | optimistic echo + 30s 心跳保活 |
| 多端订阅 fan-out 内存 | 慢客户端拖垮 cs-cloud | 每订阅者独立队列 + 背压降级 |
| AI 与用户写入冲突 | 行为不可预测 | terminal 级互斥锁 + inputLock |
| csc 进程崩溃丢失 task | taskId 失效 | registry 监听 csc 退出事件，标记所有相关 terminal 为 `disconnected` |

---

## 12. 与 MCP 路径的对比（备选方案）

曾考虑将 terminal 工具实现为独立 MCP server，**未采纳**，原因：

| 维度 | Builtin + cs-cloud 标准化 | MCP server |
|------|--------------------------|------------|
| Task 系统复用 | 直接用 LocalShellTask | 要么重写 PTY，要么反向 IPC |
| Terminal Panel UI | csc 可直接 emit 事件供 UI 渲染 | MCP 协议无 UI 组件标准 |
| Permission 流 | 走 csc 现有审批 | 黑盒，需双向事件扩展 |
| Prompt cache | 静态 tool 列表稳定 | MCP 列表动态，cache 易失效 |
| stdin 长流延迟 | 同进程函数调用 | JSON-RPC 帧化开销 |

**结论**：terminal 工具保留为 csc builtin，cs-cloud 通过事件适配暴露给外部，是当前架构下的最优解。MCP 化留作未来跨 agent 复用时的备选。

---

## 13. 待决问题（Open Questions）

1. **terminal 命名空间**：csc emit 时直接将内部 taskId 作为对外 terminalID 使用。命名规则（前缀、长度、是否含 sessionID 短码便于日志检索）由 csc 一侧定义并在 §5 契约中固化。
2. **跨 session 共享**：是否允许一个 terminal 被多个 session 订阅（如 pair programming 场景）？默认建议不允许（简化模型）。
3. **历史 output 持久化**：进程退出后的 scrollback 是否落盘？默认建议只在内存保留 5 分钟，超时即清。
4. **csc build 分发**：含 MONITOR_TOOL 的 csc build 是单独发布还是合并到主线？需要和 csc 仓库策略对齐。
5. **多 csc 实例路由**：cs-cloud 内同一 session 是否可能存在多个 csc 实例？registry 需要按 csc 实例 ID 区分 upstream 吗？

这些问题在阶段 1 文档定稿前需要明确，建议在 PR review 中讨论。
