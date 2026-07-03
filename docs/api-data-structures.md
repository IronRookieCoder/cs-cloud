# cs-cloud 对外接口数据结构说明

> 本文档描述 cs-cloud (`/api/v1/*`) 对外暴露的所有 HTTP 接口的请求/响应数据结构，
> 用于在 cs-cloud 侧添加输入校验约束。当前大部分数据校验由 agent 侧（opencode）完成，
> cs-cloud 侧仅做转发；本文档梳理所有接口的完整契约，便于后续在 cs-cloud 侧增加校验。

---

## 1. 通用结构

### 1.1 响应信封 (Response Envelope)

所有 cs-cloud 自身处理的接口统一使用以下信封格式：

```json
{
  "ok": true,
  "data": "<T>",
  "error": null
}
```

错误响应：

```json
{
  "ok": false,
  "data": null,
  "error": {
    "code": "ERROR_CODE",
    "message": "human readable message"
  }
}
```

**Go 定义** (`internal/localserver/response.go`, `internal/terminal/helpers.go`)：

| 字段 | 类型 | 说明 |
|------|------|------|
| `ok` | `bool` | 成功/失败标志 |
| `data` | `any` | 成功时的业务数据 |
| `error` | `errVal \| null` | 失败时的错误信息 |

**errVal**:

| 字段 | 类型 | 约束 |
|------|------|------|
| `code` | `string` | 错误码，大写下划线格式，如 `BAD_REQUEST`、`NOT_FOUND` |
| `message` | `string` | 面向开发者的错误描述 |

### 1.2 公共请求头

| Header | 类型 | 必填 | 说明 |
|--------|------|------|------|
| `X-Workspace-Directory` | `string` | 否 | 工作区目录的绝对路径，用于路径沙箱校验；缺失时取进程 CWD |
| `Content-Type` | `string` | POST/PATCH 必填 | 固定 `application/json` |

### 1.3 公共错误码

| HTTP Status | Code | 说明 |
|-------------|------|------|
| 400 | `BAD_REQUEST` | 请求参数错误 |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `SESSION_LIMIT` | 终端会话数超限 |
| 500 | `INTERNAL` / `INTERNAL_ERROR` / `SESSION_CREATE_FAILED` | 服务端内部错误 |
| 501 | `DEPRECATED` | 接口已废弃 |
| 503 | `UNAVAILABLE` | agent 后端不可用 |

---

## 2. Runtime 接口（本地处理）

### 2.1 GET /api/v1/runtime/health

健康检查。

**请求参数**：无

**响应 data**:

```json
{
  "status": "ok",
  "uptime": 12345,
  "version": "1.0.0"
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `status` | `string` | 固定值 `"ok"` |
| `uptime` | `int64` | 服务运行秒数，>= 0 |
| `version` | `string` | 版本号，非空 |

---

### 2.2 GET /api/v1/runtime/config

获取运行时安全配置。

**请求参数**：无

**响应 data**:

```json
{
  "allow_absolute_paths": true,
  "max_list_depth": 0,
  "allowed_operations": ["list", "read", "search"],
  "blacklist_count": 0,
  "whitelist_enabled": false
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `allow_absolute_paths` | `bool` | 是否允许绝对路径访问 |
| `max_list_depth` | `int` | 最大目录遍历深度，0 表示不限制 |
| `allowed_operations` | `[]string` | 允许的操作列表，可选值：`list`、`read`、`search` |
| `blacklist_count` | `int` | 黑名单规则数，>= 0 |
| `whitelist_enabled` | `bool` | 是否启用白名单模式 |

---

### 2.3 GET /api/v1/runtime/files

列出目录下的文件和子目录。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `path` | `string` | 否 | `"."` | 相对路径或绝对路径（受 `allow_absolute_paths` 约束）；路径不得超出工作区 |
| `recursive` | `string` | 否 | `"false"` | 仅 `"true"` 生效 |
| `limit` | `int` | 否 | `1000` | > 0 |

**响应 data**:

```json
{
  "path": "/absolute/path/to/dir",
  "entries": [
    {
      "name": "src",
      "type": "directory",
      "size": 0,
      "modified": "2024-01-01T00:00:00Z"
    },
    {
      "name": "main.go",
      "type": "file",
      "size": 1024,
      "modified": "2024-01-01T00:00:00Z"
    }
  ]
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `path` | `string` | 解析后的绝对路径 |
| `entries` | `[]fileEntry` | 文件/目录列表 |
| `entries[].name` | `string` | 条目名称，非空 |
| `entries[].type` | `string` | `"file"` 或 `"directory"` |
| `entries[].size` | `int64` | 字节数，>= 0 |
| `entries[].modified` | `time.Time (RFC3339)` | 最后修改时间 |

**校验要点**：
- `path` 解析后不得超出 `X-Workspace-Directory` 沙箱
- `path` 必须指向一个已存在的目录

---

### 2.4 GET /api/v1/runtime/files/content

读取文件内容（按行切片）。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `path` | `string` | **是** | - | 文件路径，不得超出工作区 |
| `offset` | `int` | 否 | `1` | 起始行号（1-indexed），> 0 |
| `limit` | `int` | 否 | `2000` | 最大返回行数，> 0 |

**响应 data**:

```json
{
  "path": "/absolute/path/to/file.txt",
  "content": "line1\nline2\n",
  "lines": 2,
  "offset": 1,
  "total_lines": 100
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `path` | `string` | 解析后的绝对路径 |
| `content` | `string` | 文件内容片段 |
| `lines` | `int` | 本次返回的行数，>= 0 |
| `offset` | `int` | 本次请求的起始行号，>= 1 |
| `total_lines` | `int` | 文件总行数，>= 0 |

**校验要点**：
- `path` 为必填参数
- `path` 解析后不得超出工作区沙箱
- `path` 必须指向一个已存在的文件（非目录）

---

### 2.5 GET /api/v1/runtime/find/file

按文件名模糊搜索。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `query` | `string` | 否 | `""` | 搜索关键词，小写匹配 |
| `directory` | `string` | 否 | `"."` | 搜索根目录 |
| `dirs` | `string` | 否 | `"true"` | `"false"` 时不包含目录 |
| `limit` | `int` | 否 | `10` | 范围 `[1, 200]` |

**响应 data**: `string[]` — 匹配文件的绝对路径列表。

**校验要点**：
- `limit` 限制在 `[1, 200]`
- 自动跳过 `node_modules`、`.git`、`__pycache__`、`dist`、`build`、`.cache`、`venv` 等目录

---

### 2.6 GET /api/v1/runtime/path

获取路径信息。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `directory` | `string` | 否 | `"."` | 目标目录路径 |

**响应 data**:

```json
{
  "home": "/home/user",
  "directory": "/home/user/project"
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `home` | `string` | 用户主目录，非空 |
| `directory` | `string` | 解析后的绝对路径 |

---

### 2.7 GET /api/v1/runtime/vcs

获取 Git 分支信息。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `directory` | `string` | 否 | `"."` | 目标目录路径 |

**响应 data**:

```json
{
  "branch": "main"
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `branch` | `string` | 当前 Git 分支名，非 Git 仓库时为空字符串 |

---

### 2.8 GET /api/v1/runtime/diff

获取 Git 差异统计信息（暂存 + 未暂存）。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `directory` | `string` | 否 | `"."` | 目标目录路径 |
| `path` | `string` | 否 | `""` | 按路径过滤 |

**响应 data**:

```json
{
  "directory": "/home/user/project",
  "branch": "main",
  "stagedFiles": [
    {
      "path": "src/main.go",
      "status": "modified",
      "additions": 10,
      "deletions": 3
    }
  ],
  "unstagedFiles": [
    {
      "path": "src/util.go",
      "status": "modified",
      "additions": 2,
      "deletions": 0
    }
  ]
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `directory` | `string` | 绝对路径 |
| `branch` | `string` | Git 分支名 |
| `stagedFiles` | `[]diffFileEntry` | 暂存区变更文件列表 |
| `unstagedFiles` | `[]diffFileEntry` | 未暂存变更文件列表 |
| `diffFileEntry.path` | `string` | 文件路径 |
| `diffFileEntry.status` | `string` | `"modified"` / `"deleted"` / `"renamed"` |
| `diffFileEntry.additions` | `int` | 新增行数，>= 0 |
| `diffFileEntry.deletions` | `int` | 删除行数，>= 0 |

**错误**：目标目录非 Git 仓库时返回 `400 NOT_GIT_REPO`。

---

### 2.9 GET /api/v1/runtime/diff/content

获取 Git 差异正文内容。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `staged` | `string` | 否 | `"false"` | `"true"` 时查看暂存区差异 |
| `path` | `string` | 否 | `""` | 按路径过滤 |

> 目录由 `X-Workspace-Directory` header 决定（无该 header 时取进程 CWD）。

**响应 data**:

```json
{
  "diff": "diff --git a/src/main.go b/src/main.go\nindex abc..def 100644\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1,3 +1,4 @@\n ..."
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `diff` | `string` | 完整 `git diff` 输出，无差异时为空字符串 |

---

### 2.10 POST /api/v1/runtime/dispose

销毁所有 agent 实例并重新初始化默认 agent。

**请求体**：无

**响应 data**:

```json
{
  "disposed": true
}
```

---

## 3. Agent 接口

### 3.1 GET /api/v1/agents

列出所有检测到的 agent。

**请求参数**：无

**响应 data**:

```json
{
  "agents": [
    {
      "id": "opencode",
      "name": "OpenCode",
      "driver": "http",
      "available": true,
      "endpoint": "http://127.0.0.1:12345"
    }
  ]
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `agents` | `array` | agent 列表 |
| `agents[].id` | `string` | 后端标识，非空 |
| `agents[].name` | `string` | 显示名称，非空 |
| `agents[].driver` | `string` | 驱动类型，非空 |
| `agents[].available` | `bool` | 是否可用 |
| `agents[].endpoint` | `string` | 可选，agent HTTP 地址 |

---

### 3.2 GET /api/v1/agents/health

探测所有运行中 agent 的健康状态。

**请求参数**：无（读取 `X-Workspace-Directory` header）

**响应 data**:

```json
{
  "agents": [
    {
      "id": "agent-001",
      "backend": "opencode",
      "driver": "http",
      "state": "connected",
      "available": true,
      "latency_ms": 15,
      "error": ""
    }
  ]
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `agents[].id` | `string` | agent 实例 ID |
| `agents[].backend` | `string` | 后端类型 |
| `agents[].driver` | `string` | 驱动类型 |
| `agents[].state` | `string` | 状态枚举：`idle` / `connecting` / `connected` / `session_active` / `disconnected` / `error` |
| `agents[].available` | `bool` | 是否健康 |
| `agents[].latency_ms` | `int64` | 延迟毫秒数（`available=true` 时必填） |
| `agents[].error` | `string` | 错误信息（`available=false` 时非空） |

**错误**：无运行 agent 时返回 `503 UNAVAILABLE`。

---

### 3.3 Agent 信息查询接口（代理）

以下接口由 cs-cloud 代理到 agent 后端，agent 后端路径和响应结构如下：

| cs-cloud 路径 | agent 后端路径 | 说明 |
|---------------|---------------|------|
| `GET /agents/models` | `GET /provider/capabilities` | 获取可用模型列表 |
| `GET /agents/models/config` | `GET /provider/config` | 获取当前 agent 后端/provider 配置 |
| `PATCH /agents/models/config` | `PATCH /provider/config` | 更新当前agent 后端/provider 配置 |
| `GET /agents/session-modes` | `GET /agent` | 获取 agent 会话模式 |
| `GET /agents/commands` | `GET /command` | 获取可用命令列表 |
| `GET /agents/mcp` | `GET /mcp` | 获取 MCP 状态 |
| `GET /agents/lsp` | `GET /lsp` | 获取 LSP 状态 |

这些接口直接透传 agent 后端的响应，cs-cloud 不做数据结构转换。

---

## 4. Conversation 接口（代理）

所有 Conversation 接口代理到 opencode 后端，cs-cloud 进行路径重写和请求体转换。

### 路径映射

| cs-cloud | agent 后端 | 请求体转换 |
|----------|-----------|-----------|
| `POST /conversations` | `POST /session` | 无 |
| `GET /conversations` | `GET /session` | 无 |
| `GET /conversations/status` | `GET /session/status` | 无 |
| `GET /conversations/{id}` | `GET /session/{id}` | 无 |
| `PATCH /conversations/{id}` | `PATCH /session/{id}` | 无 |
| `DELETE /conversations/{id}` | `DELETE /session/{id}` | 无 |
| `POST /conversations/{id}/abort` | `POST /session/{id}/abort` | 无 |
| `GET /conversations/{id}/messages` | `GET /session/{id}/message` | 无 |
| `GET /conversations/{id}/todo` | `GET /session/{id}/todo` | 无 |
| `POST /conversations/{id}/shell` | `POST /session/{id}/shell` | 无 |
| `POST /conversations/{id}/command` | `POST /session/{id}/command` | 无 |
| `GET /conversations/{id}/diff` | — | **已废弃**，返回 `501 DEPRECATED` |

### 4.1 POST /conversations/{id}/prompt 和 /prompt/async

**cs-cloud 侧请求体**（客户端发送）：

```json
{
  "content": "Hello",
  "files": ["path/to/file.ts"]
}
```

或新格式（含 `parts`，透传不做转换）：

```json
{
  "parts": [
    { "type": "text", "text": "Hello" }
  ],
  "files": ["path/to/file.ts"]
}
```

**cs-cloud 转换后发给 agent 的请求体**（当不含 `parts` 时）：

```json
{
  "parts": [
    { "type": "text", "text": "Hello" }
  ],
  "files": ["path/to/file.ts"]
}
```

**转换规则** (`transformPromptBody`)：
- 若请求体中已有 `parts` 字段，不做转换，直接透传
- 若请求体中无 `parts` 字段，将 `content` 转为 `parts: [{ type: "text", text: content }]`
- `files` 字段保持不变

**校验建议**：
- `content` 或 `parts` 至少提供一个
- `content` 为 `string` 类型
- `parts` 为数组，每个元素必须含 `type` 字段
- `files` 为 `string[]`

### 4.2 agent 后端 Session 相关数据结构（参考）

以下为 opencode 后端定义的数据结构，cs-cloud 代理后可直接透传，但需要了解以便未来做校验：

#### Session.Info（opencode 侧）

```typescript
{
  id: string,                    // SessionID
  slug: string,
  projectID: string,             // ProjectID
  workspaceID?: string,          // WorkspaceID
  directory: string,
  parentID?: string,             // SessionID
  summary?: {
    additions: number,
    deletions: number,
    files: number,
    diffs?: Snapshot.FileDiff[]
  },
  share?: { url: string },
  title: string,
  version: string,
  time: {
    created: number,             // Unix timestamp
    updated: number,
    compacting?: number,
    archived?: number
  },
  permission?: Permission.Ruleset,
  revert?: {
    messageID: string,
    partID?: string,
    snapshot?: string,
    diff?: string
  }
}
```

#### PromptInput（opencode 侧 `POST /session/{id}/prompt_async` 完整格式）

```typescript
{
  sessionID: string,
  messageID?: string,
  model?: { providerID: string, modelID: string },
  agent?: string,
  noReply?: boolean,
  tools?: Record<string, boolean>,     // deprecated
  format?: string,
  system?: string,
  variant?: string,
  parts: Array<{
    type: "text",
    text: string,
    synthetic?: boolean,
    ignored?: boolean,
    time?: number,
    metadata?: any
  } | {
    type: "file",
    mime: string,
    filename?: string,
    url: string,
    source?: string
  } | {
    type: "agent",
    name: string,
    source?: string
  } | {
    type: "subtask",
    prompt: string,
    description: string,
    agent: string,
    model?: { providerID: string, modelID: string },
    command?: string
  }>,
  files?: string[]
}
```

#### ShellInput（opencode 侧 `POST /session/{id}/shell`）

```typescript
{
  sessionID: string,
  agent: string,
  model?: { providerID: string, modelID: string },
  command: string
}
```

#### CommandInput（opencode 侧 `POST /session/{id}/command`）

```typescript
{
  messageID?: string,
  sessionID: string,
  agent?: string,
  model?: { providerID: string, modelID: string },
  arguments: any,
  command: string,
  variant?: string,
  parts?: any[]
}
```

#### Session.Update（opencode 侧 `PATCH /session/{id}`）

```typescript
{
  title?: string,
  time?: {
    archived?: number | null       // null = unarchive
  }
}
```

#### Session.Create（opencode 侧 `POST /session`）

```typescript
{
  parentID?: string,
  title?: string,
  permission?: Permission.Ruleset,
  workspaceID?: string
}
```

---

## 5. Event 接口（代理）

### 5.1 GET /api/v1/events

SSE 事件流，代理到 agent 的 `GET /event`。

**请求参数**：无

**响应**：`text/event-stream`，事件格式由 agent 后端定义。

**agent 后端事件结构**（opencode BusEvent）：

```typescript
{
  type: string,                          // 事件类型
  properties: {                          // 按 type 不同而不同
    sessionID?: string,
    messageID?: string,
    // ...
  }
}
```

**cs-cloud 内部事件模型** (`internal/model/event.go`):

| 字段 | 类型 | 约束 |
|------|------|------|
| `type` | `string` | 事件类型，非空 |
| `conversation_id` | `string` | 可选，关联的会话 ID |
| `msg_id` | `string` | 可选，关联的消息 ID |
| `backend` | `string` | 可选，后端标识 |
| `data` | `any` | 事件载荷 |

---

## 6. Permission 接口（代理）

### 路径映射

| cs-cloud | agent 后端 | 请求体转换 |
|----------|-----------|-----------|
| `GET /permissions` | `GET /permission` | 无 |
| `POST /permissions/{id}/reply` | `POST /permission/{id}/reply` | 字段重命名：`decision` → `reply` |

### 6.1 GET /permissions

获取待处理的权限请求列表。代理透传。

**agent 后端响应**（`Permission.Request[]`）：

```typescript
[{
  id: string,                          // 请求 ID
  sessionID: string,
  permission: string,                  // 权限标识
  patterns: string[],                  // 匹配模式
  metadata: any,
  always: boolean,
  tool?: { callID: string, tool: string, input: any }
}]
```

### 6.2 POST /permissions/{id}/reply

回复权限请求。

**cs-cloud 侧请求体**（客户端发送）：

```json
{
  "decision": "once"
}
```

**cs-cloud 转换后发给 agent 的请求体**：

```json
{
  "reply": "once"
}
```

**转换规则** (`renameJSONField`)：将 JSON key `"decision"` 重命名为 `"reply"`。

**校验建议**：
- 路径参数 `{id}` 为非空字符串
- 请求体中 `decision`（cs-cloud 侧）或 `reply`（agent 侧）为枚举值：`"once"` | `"always"` | `"reject"`

---

## 7. Question 接口（代理）

### 路径映射

| cs-cloud | agent 后端 | 请求体转换 |
|----------|-----------|-----------|
| `GET /questions` | `GET /question` | 无 |
| `POST /questions/{id}/reply` | `POST /question/{id}/reply` | 无 |
| `POST /questions/{id}/reject` | `POST /question/{id}/reject` | 无 |

### 7.1 GET /questions

获取待处理的问题列表。代理透传。

**agent 后端响应**（`Question.Request[]`）：

```typescript
[{
  id: string,
  sessionID: string,
  // ... 其他字段
}]
```

### 7.2 POST /questions/{id}/reply

回复问题。

**请求体**：由 agent 后端定义，cs-cloud 直接透传。

**校验建议**：
- 路径参数 `{id}` 为非空字符串

### 7.3 POST /questions/{id}/reject

拒绝问题。

**请求体**：由 agent 后端定义，cs-cloud 直接透传。

**校验建议**：
- 路径参数 `{id}` 为非空字符串

---

## 8. Terminal 接口（本地处理）

### 8.1 POST /api/v1/terminal

创建终端会话。

**请求体**：

```json
{
  "cwd": "/home/user/project",
  "rows": 24,
  "cols": 80
}
```

| 字段 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `cwd` | `string` | 否 | `""` | 工作目录路径 |
| `rows` | `uint16` | 否 | `24` | 终端行数，0 时取默认值 24 |
| `cols` | `uint16` | 否 | `80` | 终端列数，0 时取默认值 80 |

**校验建议**：
- `cwd` 应为有效的目录路径
- `rows` 范围 `[1, 65535]`（默认 24）
- `cols` 范围 `[1, 65535]`（默认 80）
- 并发会话上限 20（`defaultMaxSlots`）

**响应 data**:

```json
{
  "sessionId": "abc123def",
  "pid": 12345
}
```

| 字段 | 类型 | 约束 |
|------|------|------|
| `sessionId` | `string` | 会话 ID，非空 |
| `pid` | `int` | 进程 PID，> 0 |

**错误**：
- `409 SESSION_LIMIT`：达到最大并发数
- `500 SESSION_CREATE_FAILED`：PTY 创建失败

---

### 8.2 DELETE /api/v1/terminal/{id}

终止终端会话。

**路径参数**：

| 参数 | 类型 | 约束 |
|------|------|------|
| `id` | `string` | 会话 ID，非空 |

**响应 data**: `{}` （空对象）

**错误**：`404 NOT_FOUND` — 会话不存在。

---

### 8.3 POST /api/v1/terminal/{id}/resize

调整终端尺寸。

**请求体**：

```json
{
  "rows": 40,
  "cols": 120
}
```

| 字段 | 类型 | 必填 | 约束 |
|------|------|------|------|
| `rows` | `uint16` | 是 | > 0 |
| `cols` | `uint16` | 是 | > 0 |

**响应 data**: `{}`

**错误**：`404 NOT_FOUND` — 会话不存在。

---

### 8.4 POST /api/v1/terminal/{id}/restart

重启终端会话。

**请求体**：

```json
{
  "cwd": "/home/user/project"
}
```

| 字段 | 类型 | 必填 | 约束 |
|------|------|------|------|
| `cwd` | `string` | 否 | 新的工作目录 |

**响应 data**:

```json
{
  "sessionId": "abc123def",
  "pid": 12346
}
```

**错误**：`404 NOT_FOUND` — 会话不存在。

---

### 8.5 POST /api/v1/terminal/{id}/input

向终端发送输入。

**请求体**：

```json
{
  "data": "bHMgLWwK"
}
```

| 字段 | 类型 | 必填 | 约束 |
|------|------|------|------|
| `data` | `string` | 是 | Base64 编码的原始输入数据 |

**校验**：
- `data` 必须为合法的 Base64 字符串

**响应 data**: `{}`

**错误**：
- `400 BAD_REQUEST` — 无效的 Base64
- `404 NOT_FOUND` — 会话不存在

---

### 8.6 GET /api/v1/terminal/{id}/stream

SSE 终端输出流。

**请求参数** (Query String):

| 参数 | 类型 | 必填 | 默认值 | 约束 |
|------|------|------|--------|------|
| `heartbeat` | `int` | 否 | `15` | 心跳间隔秒数，> 0 |

**响应**：`text/event-stream`

SSE 事件类型：

| event | data | 说明 |
|-------|------|------|
| `connected` | `{}` | 连接建立 |
| `data` | `"<base64>"` | 终端输出数据（Base64 编码） |
| `heartbeat` | `{}` | 心跳 |
| `exit` | `{"exitCode": 0}` | 终端退出 |

---

### 8.7 GET /api/v1/terminal/input-ws

WebSocket 终端输入通道。

**协议**：

文本消息：
- JSON 控制消息（以 `{` 开头）：`{ "t": "b", "s": "sessionId", "v": 1 }`
- 普通文本：直接作为终端输入

二进制消息：
- `0x01` + JSON 控制消息：控制指令
- 其他二进制：直接作为终端输入

**控制消息类型**：

| `t` 值 | 说明 | 附加字段 |
|--------|------|----------|
| `"b"` | 绑定会话 | `s`: sessionID, `v`: 版本 |
| `"p"` | Ping | — |
| `"po"` | Pong（服务端响应） | `v`: 版本 |

---

## 9. Header 转换规则

代理请求到 agent 后端时的 Header 映射：

| cs-cloud 侧 Header | agent 后端 Header |
|--------------------|--------------------|
| `X-Workspace-Directory` | `x-opencode-directory` |

---

## 10. 代理接口校验建议总结

当前 cs-cloud 对代理接口（Conversation / Permission / Question / Event / Agent 查询）**不做请求体校验**，直接透传。建议在 cs-cloud 侧增加以下约束：

### 10.1 通用校验

- 所有 `{id}` 路径参数：非空字符串
- 所有 POST/PATCH 请求体：合法 JSON
- `Content-Type: application/json` 校验

### 10.2 Prompt 接口（POST /conversations/{id}/prompt, /prompt/async）

- 请求体必须包含 `content`（string）或 `parts`（array）之一
- `content` 时：`string` 类型，非空
- `parts` 时：`array` 类型，每个元素含 `type` 字段（`"text"` | `"file"` | `"agent"` | `"subtask"`）
- `files` 可选，`string[]`

### 10.3 Permission Reply（POST /permissions/{id}/reply）

- 请求体必须含 `decision` 字段
- `decision` 枚举值：`"once"` | `"always"` | `"reject"`

### 10.4 Terminal 创建（POST /terminal）

- `cwd`：有效路径字符串
- `rows`：`[1, 65535]`（默认 24）
- `cols`：`[1, 65535]`（默认 80）

### 10.5 Terminal Input（POST /terminal/{id}/input）

- `data`：合法 Base64 字符串

### 10.6 Terminal Resize（POST /terminal/{id}/resize）

- `rows`：`uint16`，> 0
- `cols`：`uint16`，> 0

---

## 11. 完整接口路由表

| # | Method | Path | 处理方式 | 源码位置 |
|---|--------|------|---------|---------|
| 1 | GET | `/api/v1/runtime/health` | 本地 | `health_handler.go:14` |
| 2 | GET | `/api/v1/runtime/config` | 本地 | `runtime_config.go:17` |
| 3 | GET | `/api/v1/runtime/files` | 本地 | `runtime_files.go:28` |
| 4 | GET | `/api/v1/runtime/files/content` | 本地 | `runtime_files.go:158` |
| 5 | GET | `/api/v1/runtime/find/file` | 本地 | `find_files.go:12` |
| 6 | GET | `/api/v1/runtime/path` | 本地 | `runtime_context.go:17` |
| 7 | GET | `/api/v1/runtime/vcs` | 本地 | `runtime_context.go:41` |
| 8 | GET | `/api/v1/runtime/diff` | 本地 | `runtime_diff.go:29` |
| 8a | GET | `/api/v1/runtime/diff/content` | 本地 | `runtime_diff.go:168` |
| 9 | POST | `/api/v1/runtime/dispose` | 本地 | `runtime_context.go:57` |
| 10 | GET | `/api/v1/agents` | 本地 | `agent_handlers.go:16` |
| 11 | GET | `/api/v1/agents/health` | 本地 | `agent_handlers.go:43` |
| 12 | GET | `/api/v1/agents/models` | 代理→`/provider/capabilities` | `driver_opencode.go:97` |
| 12a | GET | `/api/v1/agents/models/config` | 代理→`/provider/config` | `driver_opencode.go:98` |
| 12b | PATCH | `/api/v1/agents/models/config` | 代理→`/provider/config` | `driver_opencode.go:99` |
| 13 | GET | `/api/v1/agents/session-modes` | 代理→`/agent` | `driver_opencode.go:100` |
| 14 | GET | `/api/v1/agents/commands` | 代理→`/command` | `driver_opencode.go:99` |
| 15 | GET | `/api/v1/agents/mcp` | 代理→`/mcp` | `driver_opencode.go:100` |
| 16 | GET | `/api/v1/agents/lsp` | 代理→`/lsp` | `driver_opencode.go:101` |
| 17 | POST | `/api/v1/conversations` | 代理→`/session` | `driver_opencode.go:77` |
| 18 | GET | `/api/v1/conversations` | 代理→`/session` | `driver_opencode.go:78` |
| 19 | GET | `/api/v1/conversations/status` | 代理→`/session/status` | `driver_opencode.go:79` |
| 20 | GET | `/api/v1/conversations/{id}` | 代理→`/session/{id}` | `driver_opencode.go:80` |
| 21 | PATCH | `/api/v1/conversations/{id}` | 代理→`/session/{id}` | `driver_opencode.go:81` |
| 22 | DELETE | `/api/v1/conversations/{id}` | 代理→`/session/{id}` | `driver_opencode.go:82` |
| 23 | POST | `/api/v1/conversations/{id}/prompt` | 代理→`/session/{id}/prompt_async` + body 转换 | `driver_opencode.go:83` |
| 24 | POST | `/api/v1/conversations/{id}/prompt/async` | 代理→`/session/{id}/prompt_async` + body 转换 | `driver_opencode.go:84` |
| 25 | POST | `/api/v1/conversations/{id}/abort` | 代理→`/session/{id}/abort` | `driver_opencode.go:85` |
| 26 | GET | `/api/v1/conversations/{id}/messages` | 代理→`/session/{id}/message` | `driver_opencode.go:86` |
| 27 | GET | `/api/v1/conversations/{id}/todo` | 代理→`/session/{id}/todo` | `driver_opencode.go:87` |
| 28 | GET | `/api/v1/conversations/{id}/diff` | **已废弃** → 501 | `runtime_diff.go:158` |
| 29 | POST | `/api/v1/conversations/{id}/shell` | 代理→`/session/{id}/shell` | `driver_opencode.go:89` |
| 30 | POST | `/api/v1/conversations/{id}/command` | 代理→`/session/{id}/command` | `driver_opencode.go:90` |
| 31 | GET | `/api/v1/events` | 代理→`/event` (SSE) | `driver_opencode.go:96` |
| 32 | GET | `/api/v1/permissions` | 代理→`/permission` | `driver_opencode.go:91` |
| 33 | POST | `/api/v1/permissions/{id}/reply` | 代理→`/permission/{id}/reply` + 字段重命名 | `driver_opencode.go:92` |
| 34 | GET | `/api/v1/questions` | 代理→`/question` | `driver_opencode.go:93` |
| 35 | POST | `/api/v1/questions/{id}/reply` | 代理→`/question/{id}/reply` | `driver_opencode.go:94` |
| 36 | POST | `/api/v1/questions/{id}/reject` | 代理→`/question/{id}/reject` | `driver_opencode.go:95` |
| 37 | POST | `/api/v1/terminal` | 本地 | `terminal/handlers.go:46` |
| 38 | DELETE | `/api/v1/terminal/{id}` | 本地 | `terminal/handlers.go:85` |
| 39 | POST | `/api/v1/terminal/{id}/resize` | 本地 | `terminal/handlers.go:94` |
| 40 | POST | `/api/v1/terminal/{id}/restart` | 本地 | `terminal/handlers.go:108` |
| 41 | GET | `/api/v1/terminal/{id}/stream` | 本地 (SSE) | `terminal/handlers.go:145` |
| 42 | POST | `/api/v1/terminal/{id}/input` | 本地 | `terminal/handlers.go:124` |
| 43 | GET | `/api/v1/terminal/input-ws` | 本地 (WebSocket) | `terminal/input_ws.go:30` |
