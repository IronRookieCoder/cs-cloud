# cs-bridge 会话消息接口规范

> 本文档定义 cs-bridge 暴露的会话/消息/SSE 接口契约，供任何消费方（Web 端、桌面端、第三方集成）对接。文中"消费方"指代任一接入方，与具体语言/框架无关。

## 1. 整体架构

```
┌─────────────────────────────────────────────────┐
│  消费方（任一前端 / 桌面端 / 第三方集成）          │
│  - UI 渲染                                       │
│  - 本地状态缓存                                  │
│  - SSE 事件分发 / 重连 / 看门狗                   │
└───────────────┬─────────────────────────────────┘
                │
        ┌───────┴────────┐
        │ HTTP            │ SSE
        ▼                 ▼
┌─────────────────────────────────────────────────┐
│                  cs-bridge                       │
│  REST: /api/v1/conversations/...                │
│  SSE:  /api/v1/events                            │
└─────────────────────────────────────────────────┘
```

接口分两大类：
- **REST**：会话/消息/任务/diff/todo/权限/问答的同步读写
- **SSE**：`/api/v1/events` 单一长连接，所有会话级、消息级、运行时级事件都通过它推送

## 2. REST 接口契约

所有路由前缀 `/api/v1`。会话相关路由：

| 方法 | 路径 | 用途 |
|------|------|------|
| GET  | `/conversations` | 会话列表（支持 `roots`、`limit`、`directory` query） |
| GET  | `/conversations/status` | 所有会话状态映射 `{ [sid]: SessionStatus }` |
| POST | `/conversations` | 创建会话 |
| GET  | `/conversations/:id` | 单个会话详情 |
| PATCH| `/conversations/:id` | 更新（标题等） |
| DELETE| `/conversations/:id` | 删除 |
| POST | `/conversations/:id/abort` | 中止当前生成 |
| POST | `/conversations/:id/prompt` | 同步 prompt |
| POST | `/conversations/:id/prompt/async` | 异步 prompt（实际使用） |
| **GET** | **`/conversations/:id/messages`** | **拉取历史消息（含 parts）** |
| GET  | `/conversations/:id/todo` | Todo 列表 |
| GET  | `/conversations/:id/tasks` | 任务列表 |
| GET  | `/conversations/:id/diff` | 文件 diff |
| POST | `/conversations/:id/shell` `/command` | 执行 shell/command |
| GET  | `/permissions` `/questions` | 列出待响应的权限/问题 |
| POST | `/permissions/:id/reply` | 权限响应（decision: `once`/`always`/`reject`） |
| POST | `/questions/:id/reply` `/reject` | 问题响应 |

> Auth：所有请求需携带认证头（消费方实现细节，常见为 cookie + Authorization 头）。多工作区场景下可选 `X-Workspace-Directory: <encodeURIComponent(dir)>` 标识目标工作区。

### 2.1 messages 响应格式

**HTTP**：`GET /conversations/:id/messages?limit=N`

**响应体**：JSON 数组（顶层就是 `[...]`，**不是** `{ messages: [...] }` 包装），数组每项是 `{ info, parts }` 结构：

```ts
type MessageListResponse = Array<MessageItem>

type MessageItem = {
  info: Message          // 一条完整消息（UserMessage | AssistantMessage，结构见 §4.2 / §4.3）
  parts: Part[]          // 该消息的所有 Part（结构见 §4.4）；可能为空数组
}
```

**字段细节**：

- `info.role`：`"user"` 或 `"assistant"`
- `info.id` / `info.sessionID`：消息 ID 与所属会话 ID
- `info.time`：`{ created: number; completed?: number }`（毫秒时间戳）。assistant 消息完成后会带 `completed`
- `info.agent` / `info.model`：生成该消息的 agent 和模型信息
- `parts`：Part 数组，按 part 在消息中的顺序排列；空数组表示该消息无 part 内容（如纯错误消息）

**消息过滤规则**（cs-bridge 在响应中已剔除）：
- 本地命令消息（local command message）
- interrupt 消息（用户中断信号；同时会把上一条 assistant 消息标记为 aborted）

**user 消息空 part 跳过**：若某 user 消息没有任何 part，cs-bridge 不会返回该消息。

**示例响应**：

```json
[
  {
    "info": {
      "id": "msg-1",
      "sessionID": "sess-abc",
      "role": "user",
      "time": { "created": 1722000000000 },
      "agent": "build",
      "model": { "providerID": "anthropic", "modelID": "claude-sonnet-4-5" }
    },
    "parts": [
      { "id": "p-1", "type": "text", "text": "你好", "sessionID": "sess-abc", "messageID": "msg-1" }
    ]
  },
  {
    "info": {
      "id": "msg-2",
      "sessionID": "sess-abc",
      "role": "assistant",
      "parentID": "msg-1",
      "time": { "created": 1722000001000, "completed": 1722000005000 },
      "agent": "build",
      "modelID": "claude-sonnet-4-5",
      "providerID": "anthropic",
      "tokens": { "input": 100, "output": 50, "reasoning": 0, "cache": { "read": 0, "write": 0 } },
      "cost": 0.001
    },
    "parts": [
      { "id": "p-2", "type": "text", "text": "你好！有什么可以帮你的？", "sessionID": "sess-abc", "messageID": "msg-2" }
    ]
  }
]
```

**排序规则**：消费方按 `info.time.created` 升序排序，cs-bridge 返回顺序不重要。

分页参数：`?limit=N`。**重要**：消费方典型用法是首次加载后仅依赖 SSE 增量更新，超过首批历史的增量分页不会被触发。因此 cs-bridge 至少要保证 `limit=200` 能覆盖最近一轮完整会话，或双方约定扩展分页协议。

### 2.2 典型读取流程

1. 进入会话：`GET /conversations/:id/messages?limit=200` → 写入 `messages[sid]` 和 `parts[messageID]`
2. 发 prompt：`POST /conversations/:id/prompt/async`（body 含 `parts`、agent、model 等）
3. 之后所有更新都通过 `/events` SSE 推送，不再轮询 messages

## 3. SSE 流契约（核心）

### 3.1 连接

- 端点：`GET /events`（单一全局流，所有会话/运行时事件都通过它推送）
- 请求头：
  ```
  Accept: text/event-stream
  X-Workspace-Directory: <encoded>   （可选）
  ...auth headers
  ```
- 认证：cookie credentials + auth header

### 3.2 帧格式

标准 SSE 帧：每行以 `data: ` 前缀开始，载荷是 JSON。

```
data: {"directory": "...", "payload": { ...事件... } }
```

消费方解析约束：
- 单行 JSON
- 按 `\n` 切行，只取 `data: ` 前缀的行
- 解析后取 `event.payload ?? event`（兼容裸 payload）
- 流式读取：用 `TextDecoder` + 行缓冲（按 `\n` 切，最后一段留作下次缓冲）

### 3.3 心跳/活性

**cs-bridge 必须在 30s 内至少发一帧**。推荐每 10-15s 发一次心跳：`data: {"type":"heartbeat"}`（消费方遇到无 `type` 或未知 `type` 的 payload 会跳过）。

消费方典型活性策略（可选实现）：
- 30 秒活跃超时
- 每收到任一 SSE 帧就重置定时器
- 超时无帧 → 触发重连；连续失败若干次（推荐 5 次）→ 标记后端不可用并停止重连

### 3.4 事件载荷总规范

```ts
type Event<T = unknown> = {
  type: string                     // 事件名（dispatch key，大小写敏感）
  sessionID?: string               // 顶层 sessionID（首选）
  properties: T & {                // 事件具体数据
    sessionID?: string             // properties.sessionID（备选）
    info?: any
    part?: any
    ...
  }
}
```

**sessionID 提取链**（消费方按以下顺序回退）：
```
payload.sessionID
  ?? properties.sessionID
  ?? properties.part?.sessionID
  ?? properties.info?.sessionID
  ?? properties.status?.sessionID
  ?? properties.diff?.[0]?.sessionID
  ?? properties.todos?.[0]?.sessionID
```
cs-bridge 尽量放在 `payload.sessionID` 顶层；多放几处不影响。

### 3.5 事件清单

事件分为两组：会话/运行时级（A 组），消息/part 级（B 组）。消费方可能在不同模块分别订阅，但都从同一个 SSE 流接收。

**A 组：会话与运行时事件**

| `type` | properties 字段 | 语义 |
|--------|----------|-----------|
| `session.created` | `info: Session` | 新会话创建 |
| `session.updated` | `sessionID`, `info?: Session`（或 properties 本身就是部分字段） | 会话元数据变更（标题等），支持部分字段深合并 |
| `session.deleted` | `sessionID` 或 `info.id` | 会话删除 |
| `session.status` | `sessionID`, `status: SessionStatus` | 会话运行态切换（idle/busy/retry） |
| `session.error` | `sessionID`, `error?`, `message?` | 会话级错误 |
| `question.asked` | `QuestionRequest` 全字段 | 后端向用户征询多选/自定义答案 |
| `question.replied` / `question.rejected` | `sessionID`, `requestID` | 问题已被响应/拒绝 |
| `permission.asked` | `PermissionRequest` 全字段 | 后端请求权限放行（文件写、命令执行等） |
| `permission.replied` | `sessionID`, `requestID` | 权限已被响应 |
| `host.git.branch.changed` | `new_branch`, `repo_path` | 当前仓库分支切换 |
| `host.git.commit` / `status.changed` / `remote.changed` / `stash.changed` | `repo_path` | git 状态变化 |
| `agent.runtime.restarted` | — | 后端 agent 重启完成，消费方应重新拉取所有列表/状态 |

**B 组：消息与 Part 事件（消息流核心）**

| `type` | properties 字段 | 语义 |
|--------|----------|-----------|
| `message.updated` | `sessionID`, `info: Message` | 消息创建/更新。**特殊合并**：消费方在旧 msg 已有 `time.created`、新 msg 只带 `time.completed` 时，会合并保留 `created` |
| `message.part.updated` | `sessionID`, `part: Part`, `time` | Part 全量更新，幂等（见 §3.6） |
| `message.part.delta` | `messageID`, `partID`, `field`, `delta: string` | Part 字段流式增量。`field === "input"` 且 `part.type === "tool"` 时写到 `state.input`；否则直接 `part[field] += delta` |
| `message.removed` | `sessionID`, `messageID` | 消息删除 |
| `session.diff` | `sessionID`, `diff: FileDiff[]` | 文件 diff 更新 |
| `todo.updated` | `sessionID`, `todos: Todo[]` | Todo 列表更新 |
| `tool.progress` | `sessionID`, `toolUseID`/`parentToolUseID`, `data` | 工具执行进度增量（追加到 `toolProgress[toolUseID]`） |
| `task.started` | `sessionID`, `taskID`, `description?`, `taskType?` | 后台任务开始 |
| `task.progress` | `sessionID`, `taskID`, `description?`, `usage?`, `summary?` | 后台任务进度更新 |
| `task.completed` | `sessionID`, `taskID`, `status?`, `summary?`, `usage?` | 后台任务完成（status ∈ completed/failed/stopped） |

### 3.6 Part 合并语义（`message.part.updated`）

`message.part.updated` 是**幂等 upsert + 局部合并**：

```
1. 按 part.id 命中 → 替换
   但若新 part.state.output === undefined 且旧的有 output，
   保留旧的 output（流式更新不要清空已写输出）

2. 否则若 part 有 callID，按 callID 匹配
   命中 → 替换（保留原 id）

3. 都不命中 → 追加到 message.parts 末尾
```

**额外副作用**（消费方对特定工具的常见联动约定）：
- `tool === "todowrite"` 且 `state.input.todos` 存在 → 同步写到 session.todos
- 有 `callID` 且 `state.status === "completed"|"error"` → 清空 `partProgress[callID]`
- 有 `callID` 且 `state.progress: string[]` → 写入 `partProgress[callID]`（保留最后 10 条）

### 3.7 完整流式示例

下面是一次完整 prompt→生成→完成 的 SSE 流，消费方按行读取，只解析 `data: ` 前缀行。`payload` 字段就是 §3.4/§3.5 描述的事件对象。

```
data: {"type":"heartbeat"}

data: {"type":"session.status","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","status":{"type":"busy"}}}

data: {"type":"message.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","info":{"id":"msg-u1","sessionID":"sess-abc","role":"user","time":{"created":1722000001000},"agent":"build","model":{"providerID":"anthropic","modelID":"claude-sonnet-4-5"}}}}

data: {"type":"message.part.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","part":{"id":"p-a1","sessionID":"sess-abc","messageID":"msg-a1","type":"text","text":"","time":{"start":1722000001500}}}}

data: {"type":"message.part.delta","sessionID":"sess-abc","properties":{"messageID":"msg-a1","partID":"p-a1","field":"text","delta":"你好"}}

data: {"type":"message.part.delta","sessionID":"sess-abc","properties":{"messageID":"msg-a1","partID":"p-a1","field":"text","delta":"！"}}

data: {"type":"message.part.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","part":{"id":"p-a1","sessionID":"sess-abc","messageID":"msg-a1","type":"text","text":"你好！","time":{"start":1722000001500,"end":1722000002500}}}}

data: {"type":"message.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","info":{"id":"msg-a1","sessionID":"sess-abc","role":"assistant","parentID":"msg-u1","time":{"created":1722000001500,"completed":1722000002500},"agent":"build","modelID":"claude-sonnet-4-5","providerID":"anthropic","cost":0.001,"tokens":{"input":120,"output":8,"reasoning":0,"cache":{"read":0,"write":0}}}}}

data: {"type":"session.status","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","status":{"type":"idle"}}}
```

**事件时序解读**：

| 帧 | 语义 | 消费方典型动作 |
|----|------|---------------|
| `heartbeat` | 心跳，重置 30s 活跃定时器 | 仅重置定时器，无 UI 变化 |
| `session.status` busy | 会话进入生成态 | 禁用发送按钮，显示 loading |
| `message.updated`（user） | user 消息确认（替换本地乐观消息） | 按 `info.id` 替换本地占位 |
| `message.part.updated`（空 text） | assistant 新建一个 text part | 在 message 末尾追加 part |
| `message.part.delta` ×N | text 流式增量 | 把 `delta` 追加到 `parts[messageID][partID].text` |
| `message.part.updated`（text 完成） | text part 全量更新（最终内容） | 整体替换该 part |
| `message.updated`（assistant, `time.completed`） | assistant 消息完成 | 标记该 message 不再 streaming |
| `session.status` idle | 会话回到空闲 | 启用发送按钮 |

**工具调用流式**（`field: "input"`，对应 ToolPart 的 `state.input`）：

```
data: {"type":"message.part.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","part":{"id":"p-t1","sessionID":"sess-abc","messageID":"msg-a2","type":"tool","callID":"call-1","tool":"read","state":{"status":"running","input":{},"time":{"start":1722000003000}}}}}

data: {"type":"message.part.delta","sessionID":"sess-abc","properties":{"messageID":"msg-a2","partID":"p-t1","field":"input","delta":"{\"file_path\":\"/foo.txt\"}"}}

data: {"type":"message.part.updated","sessionID":"sess-abc","properties":{"sessionID":"sess-abc","part":{"id":"p-t1","sessionID":"sess-abc","messageID":"msg-a2","type":"tool","callID":"call-1","tool":"read","state":{"status":"completed","input":{"file_path":"/foo.txt"},"output":"file contents here","title":"read /foo.txt","time":{"start":1722000003000,"end":1722000003100}}}}}
```

解读：第一个 `part.updated` 建立空 running 的 tool part；`delta` 把 `field:"input"` 的内容追加到 `state.input`（流式拼接 JSON 字符串）；最后的 `part.updated` 用完成态替换，消费方据此把状态从 running 转为 completed。

## 4. 数据模型（cs-bridge 必须按此输出）

> 以下是 cs-bridge 通过 REST 响应体和 SSE 事件载荷输出的数据结构定义。所有字段名严格区分大小写。

### 4.1 `SessionStatus`

```ts
type SessionStatus =
  | { type: "idle" }
  | { type: "busy" }
  | { type: "retry"; attempt: number; message: string; next: number }
```

### 4.2 `UserMessage`

```ts
type UserMessage = {
  id: string
  sessionID: string
  role: "user"
  time: { created: number }
  format?: OutputFormat
  summary?: { title?: string; body?: string; diffs: Array<FileDiff> }
  agent: string
  model: { providerID: string; modelID: string }
  system?: string
  tools?: { [key: string]: boolean }
  variant?: string
}
```

### 4.3 `AssistantMessage`

```ts
type AssistantMessage = {
  id: string
  sessionID: string
  role: "assistant"
  time: { created: number; completed?: number }   // ⚠️ completed 是消费方判定"已完成"的关键字段
  error?: ProviderAuthError | UnknownError | MessageOutputLengthError
        | MessageAbortedError | StructuredOutputError
        | ContextOverflowError | ApiError
  parentID: string
  modelID: string
  providerID: string
  mode: string
  agent: string
  path: { cwd: string; root: string }
  summary?: boolean
  cost: number
  tokens: {
    total?: number
    input: number
    output: number
    reasoning: number
    cache: { read: number; write: number }
  }
  structured?: unknown
  variant?: string
  finish?: string
}

type Message = UserMessage | AssistantMessage
```

### 4.4 `Part` 联合类型

```ts
type Part =
  | TextPart          // type: "text"
  | SubtaskPart       // type: "subtask"
  | ReasoningPart     // type: "reasoning"
  | FilePart          // type: "file"
  | ToolPart          // type: "tool"
  | StepStartPart     // type: "step-start"
  | StepFinishPart    // type: "step-finish"
  | SnapshotPart      // type: "snapshot"
  | PatchPart         // type: "patch"
  | AgentPart         // type: "agent"
  | RetryPart         // type: "retry"
  | CompactionPart    // type: "compaction"
```

所有 Part 共有：`id`、`sessionID`、`messageID`、`type`。

#### TextPart

```ts
{
  type: "text"
  text: string
  synthetic?: boolean     // 合成消息（如系统注释）
  ignored?: boolean       // 被忽略（不计入上下文）
  time?: { start: number; end?: number }
  metadata?: { [key: string]: unknown }
}
```

#### ReasoningPart

```ts
{
  type: "reasoning"
  text: string
  metadata?: { [key: string]: unknown }
  time: { start: number; end?: number }    // 注意：必填
}
```

#### FilePart

```ts
{
  type: "file"
  mime: string
  filename?: string
  url: string
  source?: FileSource | SymbolSource | ResourceSource
}
```

#### ToolPart

```ts
{
  type: "tool"
  callID: string                              // ⚠️ 关键：用于按 callID 合并和 partProgress 跟踪
  tool: string                                // 工具名，如 "todowrite"、"read"...
  state: ToolState
  metadata?: { [key: string]: unknown }
}

type ToolState =
  | { status: "pending";  input: object; raw: string }
  | { status: "running";  input: object; title?: string; metadata?: object;
      progress?: string[];                              // 进度行
      time: { start: number } }
  | { status: "completed"; input: object; output: string; title: string;
      metadata: object;
      time: { start: number; end: number; compacted?: number };
      attachments?: Array<FilePart> }
  | { status: "error";    input: object; error: string; metadata?: object;
      time: { start: number; end: number } }
```

#### 其他 Part（节选关键字段）

```ts
SubtaskPart     { type: "subtask";    prompt; description; agent; model?; command? }
StepStartPart   { type: "step-start"; snapshot? }
StepFinishPart  { type: "step-finish"; reason; snapshot?; cost; tokens: {...} }
SnapshotPart    { type: "snapshot";   snapshot }
PatchPart       { type: "patch";      hash; files: string[] }
AgentPart       { type: "agent";      name; source? }
RetryPart       { type: "retry";      attempt; error; time: { created } }
CompactionPart  { type: "compaction"; auto; overflow? }
```

### 4.5 `Session`

```ts
type Session = {
  id: string
  slug: string
  projectID: string
  workspaceID?: string
  directory: string
  parentID?: string                              // 子会话（subtask）才有
  summary?: { additions: number; deletions: number; files: number; diffs?: Array<FileDiff> }
  share?: { url: string }
  title: string
  version: string
  time: { created: number; updated: number; compacting?: number; archived?: number }
  permission?: PermissionRuleset
  // ...（其他元数据字段）
}
```

## 5. 消费方实现要点

> 本章描述消费方与 cs-bridge 对接时需要承担的状态管理职责，与具体框架（React/Solid/Vue/原生）无关。

### 5.1 推荐数据模型

```
- session:    Record<sid, Session>
- messages:   Record<sid, Message[]>
- parts:      Record<messageID, Part[]>       ← 按 messageID 索引（不是 sessionID）
- status:     Record<sid, SessionStatus>
- diffs:      Record<sid, FileDiff[]>
- todos:      Record<sid, Todo[]>
- errors:     Record<sid, SessionError>
- tasks:      Record<sid, Record<taskID, TaskState>>
- permissions/questions: Record<sid, ...[]>
- toolProgress:  Record<toolUseID, string>    ← 增量累积的工具输出
- partProgress:  Record<callID, string[]>      ← 工具执行进度行
```

### 5.2 会话运行态

`status.type ∈ "idle" | "busy" | "retry"`：
- **idle**：会话空闲，可发新 prompt
- **busy**：正在生成（消费方应禁用发送、显示流式状态）
- **retry**：重试中（携带 `attempt`/`message`/`next` 元数据，消费方可显示倒计时）

由 `session.status` 事件驱动。若消费方实现看门狗（推荐）：对长时间 busy 但未收到 idle 的会话，周期性或页面可见时主动 GET `/conversations/status` 修正——cs-bridge 偶发漏事件时可自愈。

### 5.3 流式态判定

消费方通常用最后一条 assistant 消息的 `time.completed` 字段判断是否还在生成：

```
isStreaming = 最后一条 assistant message 的 time.completed === undefined
```

→ **cs-bridge 必须在 assistant 消息完成时发 `message.updated` 携带 `info.time.completed`**，否则消费方会一直显示在生成中。

### 5.4 乐观更新（推荐模式）

发 prompt 时：
1. 立即构造一个 user `Message`（`role:"user"`, `time.created = Date.now()`，本地生成 `messageID`）
2. 插入本地 store，UI 立即显示
3. 真实 message 经 SSE `message.updated` 事件到达后，按 `info.id` 替换（或按时间排序插入）
4. 失败时回滚

### 5.5 Abort 取消

消费方调用 `POST /conversations/:id/abort` 中止当前生成。**cs-bridge 必须实现该路由**。

### 5.6 历史加载

- 首次进入 session：`GET /conversations/:id/messages?limit=200`
- 之后所有更新来自 SSE——契约不强制要求增量分页，cs-bridge 至少要保证首屏能覆盖一轮完整会话

### 5.7 请求去重

`GET /messages` 这类幂等请求在并发触发时应去重（按 sessionID 复用进行中的 Promise），避免重复请求。

## 6. cs-bridge 已实现确认

cs-bridge 已实现本文涉及的全部接口与事件。下表为对接前可对照的"已就绪能力清单"：

| 能力 | 实现位置（仅供核对） | 状态 |
|------|----------------------|------|
| §2 全部 REST 路由 | 路由注册（共 20 条会话相关路由） | ✅ |
| messages 响应 `{ info, parts? }` 数组结构 | `GET /conversations/:id/messages` 代理层 | ✅ |
| `/events` SSE 流 | `GET /events` → 后端 `/event` 代理 | ✅ |
| SSE 事件 `message.updated` | 适配层多路径产出 | ✅ |
| SSE 事件 `message.part.updated`（含 callID 合并） | 适配层多路径产出 | ✅ |
| SSE 事件 `message.part.delta`（流式增量） | 流式适配层 | ✅ |
| SSE 事件 `session.status`（idle/busy 成对） | 流式 + 消息适配层 | ✅ |
| `session.created` / `updated` / `deleted` | 会话生命周期 | ✅ |
| `question.*` / `permission.*` 事件 | 权限/问答代理 | ✅ |
| `session.error` | 错误事件适配 | ✅ |
| `/abort` 路由 | `POST /conversations/:id/abort` → 后端 `/abort` | ✅ |
| Part/Message/Session/SessionStatus 数据模型 | 全部按 §4 输出 | ✅ |

### 6.1 对接注意事项

下列是 cs-bridge 已实现，但消费方在对接时仍需遵守的契约约束（变更前需双方协商）：

- SSE 心跳：保证 30s 内至少一帧（推荐 10-15s 周期发 `data: {"type":"heartbeat"}`）
- `sessionID` 同时放 `payload.sessionID` 和 `properties.sessionID`（消费方按多级回退提取）
- `message.part.delta` 的 `field` 通常是 `"text"`（追加 text part）或 `"input"`（tool 流式输入）
- `session.updated` 的 `properties` 本身支持部分字段（消费方做深合并）
- assistant 消息完成时 `message.updated` 携带 `info.time.completed`
- 错误事件 `session.error` 结构：`{ message, level?, subtype?, retryInMs?, retryAttempt?, maxRetries? }`

### 6.2 实现约束（cs-bridge 侧应持续遵守）

- 不要在 `message.part.updated` 里清掉之前已发过的 `state.output`（流式更新应保留）
- 不要把 part 的 `id` 留空（消费方用 id 做匹配）
- 不要在 `session.status` 之外用其他事件名改会话状态

### 6.3 可选增强事件（已支持，消费方可选择订阅）

`task.started/progress/completed`、`todo.updated`、`tool.progress`、`session.diff`、`host.git.*`、`agent.runtime.restarted`——这些事件 cs-bridge 已实现，但消费方未订阅也不会影响基础会话能力。

## 7. 兼容性说明

- **状态防抖**：消费方通常对 `session.updated`/`session.status(busy)` 做短延迟（约 150ms）防抖，cs-bridge 不必自己做。
- **看门狗自愈**：消费方实现看门狗时，允许 cs-bridge 偶发漏发 idle/busy；前端会反查 `/conversations/status` 修正。
- **数据模型权威**：所有字段定义以本文 §4 为准；如遇字段语义歧义以本文为准。
