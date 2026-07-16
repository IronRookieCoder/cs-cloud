# Issue 页面会话串联设计文档

> 日期：2026-07-16  
> 状态：待实现评审  
> 关联 PR：cs-cloud #24（cs-workflow 迁移）

---

## 1. 背景与目标

在 cs-cloud 完成 cs-workflow 迁移后，issue 页面需要能够像 workspace 页面一样打开并使用本地 Agent 会话。核心诉求：

1. issue 页面可以通过 issue id 查询到对应的 workspace directory 和 conversation，前端 UI 可直接跳转到该对话，最好直接拿到事件流地址。
2. cs-cloud 和设备本地不保存 issue → conversation 的映射状态。
3. issue 页面的对话体验与 workspace 页面保持一致。

---

## 2. 关键决策

| # | 问题 | 选择 |
|---|------|------|
| 1 | issue → conversation 映射存在哪里 | **multica 后端** |
| 2 | event 流如何返回 | **multica 只返回 Gateway event URL，前端直连 Gateway** |
| 3 | conversation 创建方式 | **multica 同步调 cs-cloud `POST /conversations` 创建** |

---

## 3. 总体架构

新增一个 **multica 后端接口**：

```http
GET /api/workspaces/{workspaceID}/issues/{issueID}/session
```

该接口由 multica 后端实现，职责：

1. 查询 multica 本地 `issue_id → conversation_id` 映射表。
2. 若存在有效 conversation，直接返回。
3. 若不存在，通过 Gateway 调 cs-cloud `POST /api/v1/conversations` 创建新会话，保存返回的 `conversation_id`。
4. 返回给前端：
   ```json
   {
     "conversation_id": "conv-xxx",
     "workspace_directory": "/path/to/repo",
     "events_url": "/cloud-api/cloud/device/{deviceID}/proxy/api/v1/events?conversation_id=conv-xxx"
   }
   ```

cs-cloud 侧**不新增业务代码**，只复用现有接口：

- `POST /api/v1/conversations`
- `GET /api/v1/events`
- `X-Workspace-Directory` 请求头

前端拿到 `conversation_id` 和 `events_url` 后，复用现有 workspace 对话组件。

---

## 4. 数据流

### 4.1 首次打开 issue 页面

```text
Issue 页面
   │
   ▼
GET multica /api/workspaces/{ws}/issues/{issueID}/session
   │
   ▼ multica 发现无映射
   │
   ▼ multica 构造 conversation 创建请求
   │
POST Gateway /cloud-api/cloud/device/{deviceID}/proxy/api/v1/conversations
Headers:
  X-Workspace-Directory: /path/to/repo
Body:
  { "agent": "csc", "initial_prompt": "Issue #123: ..." }
   │
   ▼ Gateway → cs-cloud localserver → Agent 后端 (csc/cs)
   │
   ▼ 返回 { "id": "conv-xxx" }
   │
   ▼ multica 保存 issue_id → conv-xxx
   │
   ▼ 返回给前端
   { conversation_id, workspace_directory, events_url }
```

### 4.2 后续打开 issue 页面

```text
Issue 页面
   │
   ▼
GET multica /api/workspaces/{ws}/issues/{issueID}/session
   │
   ▼ multica 查到已有 conv-xxx
   │
   ▼ 直接返回 { conversation_id, workspace_directory, events_url }
```

### 4.3 前端对话交互

前端复用 workspace 对话组件：

```text
Issue 页面聊天窗口
   │
   ├── 发送消息 ──► POST Gateway /proxy/api/v1/conversations/{convID}/prompt
   │
   ├── 获取历史 ──► GET  Gateway /proxy/api/v1/conversations/{convID}/messages
   │
   └── 实时事件 ──► GET  Gateway /proxy/api/v1/events?conversation_id=convID
```

---

## 5. 接口契约与错误处理

### 5.1 multica 新增接口

```http
GET /api/workspaces/{workspaceID}/issues/{issueID}/session
```

**请求头**：

- `Authorization: Bearer <user-token>`（multica 校验用户权限）

**响应 200**：

```json
{
  "conversation_id": "conv-xxx",
  "workspace_directory": "/path/to/repo",
  "events_url": "/cloud-api/cloud/device/{deviceID}/proxy/api/v1/events?conversation_id=conv-xxx"
}
```

**响应 404**：issue 不存在或用户无权限。  
**响应 503**：cs-cloud 设备不在线，无法创建 conversation。

### 5.2 创建 conversation 请求体

复用现有 `POST /conversations` 格式，multica 后端根据 issue 元数据构造：

```json
{
  "agent": "csc",
  "workspace_directory": "/path/to/repo",
  "initial_prompt": "Issue #123: ...\n描述：..."
}
```

`X-Workspace-Directory` 头同时带上 `/path/to/repo`，确保 Agent 后端和 cs-cloud event bus 都能识别工作区。

### 5.3 错误处理

| 场景 | 行为 |
|------|------|
| cs-cloud 在线，创建成功 | multica 保存映射并返回 |
| cs-cloud 不在线 | 返回 503，前端提示“设备未连接” |
| conversation 已被删除 | multica 检测到后重新创建 |
| 用户无 issue 权限 | multica 返回 403/404 |

---

## 6. 测试策略

| 层级 | 测试内容 |
|------|----------|
| multica 后端单元测试 | mock Gateway 响应，验证映射创建、查询、重建 |
| 集成测试 | 启动真实 cs-cloud + Agent 后端，验证首次请求创建 conversation，后续请求复用 |
| 前端测试 | 验证拿到 `conversation_id` 和 `events_url` 后能正确挂载现有对话组件 |

---

## 7. 待确认事项

multica 后端实现该接口时，需要能拿到：

1. **deviceID**：哪个设备上的 cs-cloud 为该用户/工作区在线。
2. **workspace_directory**：该 issue 对应的项目 repo 在本地的绝对路径。
3. **initial_prompt**：issue 标题、描述等如何拼接。

这些不由 cs-cloud 决定，由 multica/项目模型决定。

---

## 8. 下一步

本设计文档通过评审后，进入 `writing-plans` 阶段，输出 multica 后端接口的实现计划。
