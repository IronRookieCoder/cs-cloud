# cs-bridge-server 镜像使用说明

`cs-bridge-server` 是一个**裸跑版** cs-cloud + csc 一体镜像：内嵌 `cs-cloud` 调度守护进程与 `csc` agent 运行时，关掉了云端注册 / OAuth / 隧道，留出固定的 HTTP 端口（默认 `8080`）做会话与提示词调度。模型服务、Key、工作目录都通过环境变量 / 请求头注入。

- 镜像架构：`linux/arm64`、`linux/amd64`
- 内含组件：`cs-cloud` (Go)、`csc` (Bun + 内置 dist)、`tini` 作 PID 1
- 监听端口：`8080/tcp`（`CS_CLOUD_PORT` 可改）
- 工作目录沙箱：默认 `/workspace`（`CS_WORKSPACE_ROOT` 可改）

---

## 1. 拉取镜像

```bash
docker pull registry-c.cmft.com/cmhk-paz-ai-devops-chain-di/cs-bridge-server:v1.2.34-beat.1-arm64
```

按目标机器架构挑对应 tag：

| 架构 | 示例 tag |
|------|----------|
| arm64 | `v1.2.34-beat.1-arm64` |
| amd64 | `v1.2.34-beat.1-amd64` |

---

## 2. 启动容器

最小化启动（仅必需 env）：

```bash
docker run -d \
  --name cs-bridge \
  -p 8080:8080 \
  -e MODEL_PROVIDER=anthropic \
  -e MODEL_BASE_URL=https://your-model-endpoint/ \
  -e MODEL_API_KEY=sk-xxxxxxxx \
  -e MODEL_NAME=GLM-5.2 \
  registry-c.cmft.com/cmhk-paz-ai-devops-chain-di/cs-bridge-server:v1.2.34-beat.1-arm64
```

挂卷持久化（推荐，避免容器重启后会话/工作区丢失）：

```bash
docker run -d \
  --name cs-bridge \
  -p 8080:8080 \
  -v cs-bridge-data:/root/.costrict \
  -v cs-bridge-workspace:/workspace \
  -e MODEL_PROVIDER=anthropic \
  -e MODEL_BASE_URL=https://your-model-endpoint/ \
  -e MODEL_API_KEY=sk-xxxxxxxx \
  -e MODEL_NAME=GLM-5.2 \
  registry-c.cmft.com/cmhk-paz-ai-devops-chain-di/cs-bridge-server:v1.2.34-beat.1-arm64
```

启动后查看启动日志，确认 entrypoint 打印的 provider / model / perms 符合预期：

```bash
docker logs cs-bridge
# [entrypoint] cs-cloud localserver starting
# [entrypoint]   provider: anthropic
# [entrypoint]   model    : GLM-5.2
# [entrypoint]   perms    : bypass (ACP_PERMISSION_MODE=bypassPermissions, IS_SANDBOX=1)
```

---

## 3. 环境变量参考

| 变量 | 默认 | 说明 |
|------|------|------|
| `MODEL_PROVIDER` | `anthropic` | 模型协议流：`anthropic` 或 `openai`。决定下方 BASE_URL/API_KEY 的翻译目标 |
| `MODEL_BASE_URL` | — | 模型服务地址。Anthropic 流会翻译成 `ANTHROPIC_BASE_URL`；OpenAI 流翻译成 `OPENAI_BASE_URL` |
| `MODEL_API_KEY` | — | 模型 API Key。Anthropic 流 → `ANTHROPIC_AUTH_TOKEN`；OpenAI 流 → `OPENAI_API_KEY` |
| `MODEL_NAME` | — | 主模型名。`anthropic` 流 → `ANTHROPIC_MODEL`；`openai` 流 → 同时填满 `OPENAI_DEFAULT_HAIKU_MODEL` / `OPENAI_DEFAULT_SONNET_MODEL` / `OPENAI_DEFAULT_OPUS_MODEL` 三个档位槽位（多数场景一个模型够用，想分档配置请直接覆盖 `CS_CLOUD_AGENT_ENV`） |
| `BYPASS_PERMISSIONS` | `1` | `1`=自动跳过 csc 所有 permission prompt（无人值守推荐）；`0`=恢复交互式询问 |
| `CS_CLOUD_PORT` | `8080` | HTTP 监听端口 |
| `CS_CLOUD_HOST` | `0.0.0.0` | HTTP 监听地址 |
| `CS_CLOUD_DATA_DIR` | `/root/.costrict` | cs-cloud 状态目录（mode 文件、agent.pid 等） |
| `CS_WORKSPACE_ROOT` | `/workspace` | workspace 清理接口的沙箱根；清理接口只允许删此目录下的子目录 |
| `COSTRICT_CONFIG_DIR` | 同 `CS_CLOUD_DATA_DIR` | csc 用户配置目录（settings.json 落点） |
| `CS_CLOUD_AGENT_ENV` | 自动生成 | 完全覆盖 entrypoint 的翻译结果，直接以 JSON 形式给 csc 子进程注入任意环境变量。一旦设置，`MODEL_*` 不再生效 |

模型对接细节（OpenAI / Bedrock / Vertex / 自建网关）见 [localserver-model-integration.md](./localserver-model-integration.md)。

---

## 4. 核心接口

所有接口都在 `/api/v1` 前缀下。Workspace 通过 `X-Workspace-Directory` 请求头指定，**不存在时会自动 mkdir -p 创建**（用于会话隔离）。

### 4.1 创建会话

```bash
curl --request POST \
  --url http://127.0.0.1:8080/api/v1/conversations \
  --header 'x-workspace-directory: /workspace/prj-1'
```

响应示例（节选）：

```json
{
  "backend": "csc",
  "created_at": 1785239284104,
  "cwd": "/workspace/prj-1",
  "directory": "/workspace/prj-1",
  "driver": "http",
  "id": "92bb877b-b5ee-48d6-8778-39d93878934f",
  "projectID": "prj_default",
  "sessionID": "92bb877b-b5ee-48d6-8778-39d93878934f",
  "session_id": "92bb877b-b5ee-48d6-8778-39d93878934f",
  "slug": "92bb877b",
  "state": "running",
  "status": "running",
  "time": {
    "created": 1785239284104,
    "updated": 1785239284104
  },
  "title": "New session - 2026-07-28T11:48:04Z",
  "version": "1.0.0"
}
```

后续接口里把路径中的 `<conversation_id>` 换成响应里的 `id` 字段（`sessionID` / `session_id` 是同一个值）。

### 4.2 列出会话

```bash
curl --request GET \
  --url http://127.0.0.1:8080/api/v1/conversations
```

### 4.3 异步发送提示词

```bash
curl --request POST \
  --url http://127.0.0.1:8080/api/v1/conversations/<conversation_id>/prompt/async \
  --header 'content-type: application/json' \
  --header 'x-workspace-directory: /workspace/prj-1' \
  --data '{
    "agent": "build",
    "model": {
      "modelID": "GLM-5.2"
    },
    "parts": [
      {
        "type": "text",
        "text": "是的"
      }
    ]
  }'
```

字段说明：
- `agent`：选择预设 agent（如 `build`、`code` 等），决定 system prompt 与可用工具集
- `model.modelID`：覆盖本次调用使用的模型（不填则走启动时 `MODEL_NAME`）
- `parts[]`：消息部件，`type=text` 的纯文本输入是最常见形式，也可以是图片、工具结果等

`/prompt/async` 立即返回 ack，模型流式输出在会话事件流上推送；想拿结果用下方 messages 查询接口轮询，或订阅 SSE。

### 4.4 查看会话消息

```bash
curl --request GET \
  --url 'http://127.0.0.1:8080/api/v1/conversations/<conversation_id>/messages?limit=200' \
  --header 'x-workspace-directory: /workspace/prj-1'
```

### 4.5 清理 workspace

```bash
curl --request DELETE \
  --url http://127.0.0.1:8080/api/v1/workspace \
  --header 'x-workspace-directory: /workspace/prj-1'
```

也可以用 query 参数指定路径：

```bash
curl --request DELETE \
  --url 'http://127.0.0.1:8080/api/v1/workspace?dir=/workspace/prj-1'
```

返回：

```json
{ "ok": true, "data": { "deleted": "/workspace/prj-1", "existed": true } }
```

行为约束：
- 路径必须解析在 `CS_WORKSPACE_ROOT` 之下；`..` 跨界 / symlink 越界一律 400 拒绝
- 删除 workspace 根本身返回 400
- 删除不存在的目录返回 `existed:false`，幂等
- 当前**不会**自动 dispose 该 workspace 下的活跃会话——如需先停会话再删目录，请先调对应的会话终止接口

---

## 5. 常见排障

| 现象 | 排查方向 |
|------|----------|
| 启动后 `docker logs` 里 provider/model/perms 与预期不符 | entrypoint 只在 `CS_CLOUD_AGENT_ENV` 未设置时才翻译 `MODEL_*`；如果之前测试 set 过 `CS_CLOUD_AGENT_ENV`，先清掉 |
| 创建会话报 `Working directory does not exist` | 不应再出现；当前版本会自动 mkdir。如仍报错说明镜像版本旧，需更新到 `v1.2.34-beat.1` 及以上 |
| 模型调用时出现 `permission.asked` 事件 | 默认 `BYPASS_PERMISSIONS=1` 应跳过；如调过 `BYPASS_PERMISSIONS=0` 或挂了自己的 `settings.json`，请确认配置 |
| 启动报 `Address already in use` | 宿主机 8080 端口被占；改 `CS_CLOUD_PORT` 并相应改 `-p` 映射 |
| `crane pull` / `docker pull` 401 | 镜像仓库需登录：`docker login registry-c.cmft.com -u <user>` |

模型对接的更细节排障（Anthropic / OpenAI / 兼容网关）见 [localserver-model-integration.md](./localserver-model-integration.md) 第 7 节。
