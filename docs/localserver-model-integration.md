# cs-cloud localserver 镜像：模型对接指南

本文档说明 `cs-cloud-localserver` 镜像如何对接模型服务（Anthropic、OpenAI，以及任意 OpenAI-compatible / 第三方 provider）。面向已经按 `Dockerfile.localserver` 构建出镜像、准备运行容器并接入模型后端的运维/集成同学。

---

## 1. 架构回顾：谁真正调模型

```
用户请求
   │
   ▼
cs-cloud (Go daemon)        ← 只做调度/转发，不调模型
   │ 启动时 spawn csc serve 子进程
   ▼
csc (Node/Bun agent)        ← 真正发模型 API 请求的进程
   │ 用环境变量里的 key + base_url
   ▼
模型服务 (Anthropic / OpenAI / 自建网关 / Bedrock / ...)
```

cs-cloud 自己不关心模型服务在哪、用什么 key，它只负责把客户端请求转给 csc、再把 csc 的流式输出转回去。**所有模型相关配置最终都落到 csc 子进程的环境变量上**。

---

## 2. 三层环境变量

镜像里用到的环境变量分三层，理解层次能避免混淆：

| 层 | 变量 | 谁读 | 作用 |
|----|------|------|------|
| L1：入口别名 | `MODEL_PROVIDER` / `MODEL_BASE_URL` / `MODEL_API_KEY` | **entrypoint 脚本** (`scripts/docker-localserver-entrypoint.sh`) | 用户友好的统一入口，脚本翻译成 L2 |
| L2：cs-cloud 通道 | `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`） | **cs-cloud**（Go daemon, `internal/config/load.go:70-75`） | 一个 JSON map，cs-cloud 把它逐项写入 csc 子进程环境 |
| L3：csc 实际读取 | `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` / `OPENAI_BASE_URL` / `OPENAI_API_KEY` / ... | **csc** (`src/services/api/client.ts`, `src/utils/model/model.ts`) | 构造实际 HTTP 请求 |

**默认流程是 L1 → L2 → L3**：用户只设 L1，entrypoint 翻译成 L3 的 JSON 装进 L2，cs-cloud 透传给 csc。

---

## 3. 快速对接：Anthropic 流（或兼容服务）

最常见用法，csc 默认就是 anthropic 流。

### 3.1 官方 Anthropic

```bash
docker run -d -p 8080:8080 \
  -e MODEL_PROVIDER=anthropic \
  -e MODEL_API_KEY=sk-ant-xxx \
  -e ANTHROPIC_MODEL=claude-sonnet-4-5-20250929 \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

`MODEL_BASE_URL` 不设时，csc 默认走 `https://api.anthropic.com`。

### 3.2 Anthropic 兼容网关（自建/中转）

把 `MODEL_BASE_URL` 指向你的网关：

```bash
docker run -d -p 8080:8080 \
  -e MODEL_PROVIDER=anthropic \
  -e MODEL_BASE_URL=https://your-gateway.example.com \
  -e MODEL_API_KEY=sk-your-gateway-key \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 3.3 entrypoint 翻译后的等价 L3

```json
{
  "ANTHROPIC_BASE_URL": "https://your-gateway.example.com",
  "ANTHROPIC_AUTH_TOKEN": "sk-your-gateway-key"
}
```

> ⚠️ **`ANTHROPIC_AUTH_TOKEN` vs `ANTHROPIC_API_KEY`**
>
> csc 用 `ANTHROPIC_AUTH_TOKEN`（走 `Authorization: Bearer ...`），entrypoint 也写这个变量。如果你的后端要求 `x-api-key` 头（标准 Anthropic API 协议），需要用 `ANTHROPIC_API_KEY` 而非 `ANTHROPIC_AUTH_TOKEN`。绕法见 §6。

---

## 4. OpenAI 流（或任意 OpenAI-compatible 服务）

`MODEL_PROVIDER=openai`，entrypoint 会翻译成 `OPENAI_BASE_URL` + `OPENAI_API_KEY`。

### 4.1 官方 OpenAI

```bash
docker run -d -p 8080:8080 \
  -e MODEL_PROVIDER=openai \
  -e MODEL_API_KEY=sk-xxx \
  -e OPENAI_DEFAULT_SONNET_MODEL=gpt-4o \
  -e OPENAI_DEFAULT_HAIKU_MODEL=gpt-4o-mini \
  -e OPENAI_DEFAULT_OPUS_MODEL=o1 \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 4.2 OpenAI-compatible 自建网关（vLLM / DeepSeek / Moonshot / 通义千问 / 自家代理等）

```bash
docker run -d -p 8080:8080 \
  -e MODEL_PROVIDER=openai \
  -e MODEL_BASE_URL=https://api.deepseek.com/v1 \
  -e MODEL_API_KEY=sk-deepseek-xxx \
  -e OPENAI_DEFAULT_SONNET_MODEL=deepseek-chat \
  -e OPENAI_DEFAULT_HAIKU_MODEL=deepseek-chat \
  -e OPENAI_DEFAULT_OPUS_MODEL=deepseek-reasoner \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 4.3 关键坑：必须指定模型名映射

csc 内部按 Anthropic 的 tier 划分（haiku / sonnet / opus）选模型，**不会**自动适配 OpenAI 命名。如果不显式覆盖，csc 会把 `claude-sonnet-4-...` 这种 ID 发给 OpenAI 端，几乎肯定被拒。

完整模型映射变量：

| 变量 | 对应槽位 | OpenAI 官方建议 | 说明 |
|------|----------|----------------|------|
| `OPENAI_DEFAULT_HAIKU_MODEL` | 快速/小（轻量任务、辅助调用） | `gpt-4o-mini` | 必设 |
| `OPENAI_DEFAULT_SONNET_MODEL` | 主力（日常对话/代码） | `gpt-4o` 或 `gpt-4.1` | 必设 |
| `OPENAI_DEFAULT_OPUS_MODEL` | 重型（复杂推理） | `o1` 或 `gpt-4.1` | 必设 |
| `OPENAI_SMALL_FAST_MODEL` | 后台小任务（标题生成、补全等） | `gpt-4o-mini` | 可选，缺省回退到 haiku 槽 |

来源：`csc/src/utils/model/model.ts:40, 149, 179, 207`。

> 💡 这几个变量 entrypoint **不会**自动翻译（命名因部署而异，没有合理默认）。它们必须随 `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`）一起下发——下面 §6 解释两种下发方式。

---

## 5. 第三方 provider（Bedrock / Vertex / Foundry / 自定义）

csc 还支持以下 provider：

- **AWS Bedrock**：`CLAUDE_CODE_USE_BEDROCK=1` + `AWS_REGION` + AWS 凭证（`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` 或 IAM Role）
- **Google Vertex AI**：`CLAUDE_CODE_USE_VERTEX=1` + `CLOUD_ML_REGION` + `ANTHROPIC_VERTEX_PROJECT_ID` + GCP 凭证
- **Azure Foundry**：对应 Azure 的 endpoint/key 配置

这些 entrypoint 的 `MODEL_PROVIDER` switch **不认识**（会打 warning 跳过注入），必须用 §6 的方式直接给 `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`）整包。

---

## 6. 高级用法：直接覆盖 `CS_BRIDGE_AGENT_ENV`

> 历史变量名 `CS_CLOUD_AGENT_ENV` 仍作为别名生效；新部署建议用 `CS_BRIDGE_AGENT_ENV`。两者同时设置时 `CS_BRIDGE_AGENT_ENV` 优先。

当：
- 想用第三方 provider（bedrock / vertex / ...）
- 想用 `ANTHROPIC_API_KEY` 而不是 `ANTHROPIC_AUTH_TOKEN`
- 想一次性塞模型名映射 + 密钥 + base_url

直接给容器设 `CS_BRIDGE_AGENT_ENV`，**entrypoint 检测到它已设就原样透传，不再用 L1 的 `MODEL_*` 翻译**。

### 6.1 OpenAI 流 + 完整模型映射（推荐写法）

```bash
docker run -d -p 8080:8080 \
  -e 'CS_BRIDGE_AGENT_ENV={"OPENAI_BASE_URL":"https://api.openai.com/v1","OPENAI_API_KEY":"sk-xxx","OPENAI_DEFAULT_SONNET_MODEL":"gpt-4o","OPENAI_DEFAULT_HAIKU_MODEL":"gpt-4o-mini","OPENAI_DEFAULT_OPUS_MODEL":"o1"}' \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 6.2 Anthropic 但走 `x-api-key` 协议

```bash
docker run -d -p 8080:8080 \
  -e 'CS_BRIDGE_AGENT_ENV={"ANTHROPIC_BASE_URL":"https://api.anthropic.com","ANTHROPIC_API_KEY":"sk-ant-xxx"}' \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 6.3 AWS Bedrock

```bash
docker run -d -p 8080:8080 \
  -e 'CS_BRIDGE_AGENT_ENV={"CLAUDE_CODE_USE_BEDROCK":"1","AWS_REGION":"us-east-1","AWS_ACCESS_KEY_ID":"AKIA...","AWS_SECRET_ACCESS_KEY":"...","ANTHROPIC_MODEL":"us.anthropic.claude-sonnet-4-20250514-v1:0"}' \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

### 6.4 用文件挂载避免 shell 转义

JSON 写在 `-e` 里很容易被 shell 转义出错。推荐用 `--env-file`：

```bash
# model.env
CS_BRIDGE_AGENT_ENV={"OPENAI_BASE_URL":"https://api.openai.com/v1","OPENAI_API_KEY":"sk-xxx","OPENAI_DEFAULT_SONNET_MODEL":"gpt-4o","OPENAI_DEFAULT_HAIKU_MODEL":"gpt-4o-mini"}

docker run -d -p 8080:8080 --env-file ./model.env \
  -v "$PWD/workspace:/workspace" \
  cs-cloud-localserver:amd64
```

---

## 7. 校验对接是否成功

容器启动后 5~10 秒，按顺序检查：

### 7.1 daemon 是否就绪

```bash
# 看 state
docker exec <container> cat /root/.costrict/cs-cloud/state
# 期望: running

# 看 server_url
docker exec <container> cat /root/.costrict/cs-cloud/server_url
# 期望: http://127.0.0.1:8080
```

### 7.2 csc 子进程是否起来

```bash
docker exec <container> cat /root/.costrict/cs-cloud/agent.pid
# 应有非空 PID
```

### 7.3 csc 启动日志（最关键的诊断信息）

```bash
docker exec <container> cat /root/.costrict/cs-cloud/app.log | grep -E 'agent|csc|endpoint'
```

正常会看到：
```
csc/agent.go:107  [debug] spawning '/usr/local/bin/csc serve' and waiting for port...
csc/agent.go:288  [stderr] /usr/local/bin/csc serve: csc server listening on http://127.0.0.1:37537
csc/agent.go:288  [stderr] /usr/local/bin/csc serve: Max sessions: 32
csc/agent.go:288  [stderr] /usr/local/bin/csc serve: Workspace: /workspace
csc/agent.go:118  csc raw endpoint resolved: http://127.0.0.1:37537
csc/agent.go:128  csc adapter endpoint resolved: http://127.0.0.1:39931
```

### 7.4 探活 HTTP API

```bash
curl -s http://localhost:8080/api/v1/agents | jq .
# 期望: {"ok":true,"data":{"agents":[{"id":"cs","available":true},{"id":"csc","available":true}]}}
```

### 7.5 真正发一次模型请求

最直接的验证——通过 cs-cloud 转发到 csc 发起一次对话：

```bash
# 通过 csc adapter 的 /api/v1/message 或 /chat 端点（具体路径看 /api/v1/docs）
curl http://localhost:8080/api/v1/docs   # swagger UI，找 message-receive 端点
```

如果模型密钥/URL 不对，这里会返回鉴权失败、404 或超时；正确则返回 csc 的 SSE 流。

---

## 8. 常见问题排查

### 8.1 `MODEL_PROVIDER=openai` 但请求里仍是 `claude-*` 模型 ID

→ 没设 `OPENAI_DEFAULT_*_MODEL`。参考 §4.3 设上。

### 8.2 启动日志里看不到模型相关错误，但请求时 401/403

→ csc 启动时不会校验 key（它只是把 env 读进去），错误只在实际发请求时出现。打开 csc 子进程的 stderr 详细看：

```bash
docker exec <container> tail -100 /root/.costrict/cs-cloud/app.log | grep -iE 'error|401|403|auth'
```

### 8.3 entrypoint 日志显示 `agent-env: {...}` 但里面变量名不对

→ 你的 `MODEL_PROVIDER` 没匹配上 anthropic/openai，或者你设了 `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`）同时设了 `MODEL_*`（后者会被前者覆盖，前者优先）。检查 `docker logs <container>` 的 `[entrypoint]` 行。

### 8.4 csc 报 `x-api-key` 相关错误

→ 你的后端期望标准 Anthropic 协议（`x-api-key` 头），但 entrypoint 注入的是 `ANTHROPIC_AUTH_TOKEN`（`Bearer`）。改用 §6.2 直接注入 `ANTHROPIC_API_KEY`。

### 8.5 容器内能解析但请求超时

→ 检查容器 DNS 和出口：`docker exec <container> curl -v <MODEL_BASE_URL>`。如果走代理，记得给容器加 `-e HTTP_PROXY=... -e HTTPS_PROXY=...`（cs-cloud/csc 都是标准 env，会读这些）。

---

## 9. 环境变量速查表

### L1：entrypoint 输入别名（用户友好层）

| 变量 | 默认 | 取值 |
|------|------|------|
| `MODEL_PROVIDER` | `anthropic` | `anthropic` / `openai` |
| `MODEL_BASE_URL` | 空（用 csc 默认） | 模型服务的 base URL（含或不含 `/v1` 视后端） |
| `MODEL_API_KEY` | 空 | 模型服务的 API key / token |

### L2：cs-cloud 透传通道

| 变量 | 默认 | 说明 |
|------|------|------|
| `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`） | 由 entrypoint 根据 L1 翻译生成 | JSON map；已设则 entrypoint 不覆盖 |

### L3：csc 实际读取（部分常用）

| 变量 | 用途 |
|------|------|
| `ANTHROPIC_BASE_URL` | Anthropic 流的端点 |
| `ANTHROPIC_AUTH_TOKEN` | Anthropic 流的 key（Bearer） |
| `ANTHROPIC_API_KEY` | Anthropic 流的 key（x-api-key，标准协议） |
| `ANTHROPIC_MODEL` | 默认模型 ID |
| `OPENAI_BASE_URL` | OpenAI 流的端点 |
| `OPENAI_API_KEY` | OpenAI 流的 key |
| `OPENAI_DEFAULT_HAIKU_MODEL` | OpenAI 流的小模型映射 |
| `OPENAI_DEFAULT_SONNET_MODEL` | OpenAI 流的主力模型映射 |
| `OPENAI_DEFAULT_OPUS_MODEL` | OpenAI 流的重型模型映射 |
| `OPENAI_SMALL_FAST_MODEL` | 后台小任务模型 |
| `CLAUDE_CODE_USE_BEDROCK` | 启用 Bedrock（设 `1`） |
| `AWS_REGION` / `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | Bedrock 凭证 |
| `CLAUDE_CODE_USE_VERTEX` | 启用 Vertex（设 `1`） |
| `CLOUD_ML_REGION` / `ANTHROPIC_VERTEX_PROJECT_ID` | Vertex 配置 |

### 容器/daemon 自身（非模型相关，但常用）

| 变量 | 默认 | 说明 |
|------|------|------|
| `CS_BRIDGE_PORT`（旧名 `CS_CLOUD_PORT`） | `8080` | localserver 监听端口 |
| `CS_BRIDGE_HOST`（旧名 `CS_CLOUD_HOST`） | `0.0.0.0` | 监听地址 |
| `CS_BRIDGE_DATA_DIR`（旧名 `CS_CLOUD_DATA_DIR`） | `/root/.costrict` | daemon 数据目录（mode/pid/log 等） |
| `CS_BRIDGE_AGENT_PATH`（旧名 `CS_CLOUD_AGENT_PATH`） | `/usr/local/bin/csc` | csc 二进制路径 |
| `CS_BRIDGE_AUTO_UPGRADE`（旧名 `CS_CLOUD_AUTO_UPGRADE`） | `false` | 容器内不应自动升级 |
| `COSTRICT_SHARE_DIR` | `/root/.costrict/share` | 凭证目录（裸 localserver 模式下不用） |

---

## 10. 参考代码位置

| 文件 | 作用 |
|------|------|
| `scripts/docker-localserver-entrypoint.sh` | L1 → L2 翻译逻辑（`build_agent_env` 函数） |
| `internal/config/load.go:70-75` | cs-cloud 读 `CS_BRIDGE_AGENT_ENV`（旧名 `CS_CLOUD_AGENT_ENV`）JSON 到 `cfg.AgentEnv` |
| `internal/runtime/manager.go`（InitDefaultAgent） | cs-cloud 启动 csc 子进程时注入 `cfg.AgentEnv` |
| `Dockerfile.localserver` | 镜像构建逻辑（csc 安装、env 默认值、entrypoint） |
| csc: `src/services/api/client.ts:330` | csc 读 `ANTHROPIC_AUTH_TOKEN` |
| csc: `src/main.tsx:2173` | csc 读 `ANTHROPIC_BASE_URL` |
| csc: `src/commands/provider.ts:106-113` | csc 检查 openai key/url 完整性 |
| csc: `src/utils/model/model.ts:40-207` | csc 读取 `OPENAI_DEFAULT_*_MODEL` 做模型映射 |
