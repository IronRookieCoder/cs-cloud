# 附件上传契约 v2 —— 路径优先 / Agent 无关

> 本文是 [`attachment-upload-proposal.md`](./attachment-upload-proposal.md) 的**契约层补丁**。
>
> v1 提案 §4.5 假设"csc 零改动"，实际在 csc commit `b9fd73238` 中已经引入了 `file://attachments/{key}` + `CSC_ATTACHMENTS_DIR` 的耦合设计。本补丁**否定该耦合**，将契约重新锚定到"绝对路径"作为唯一对外产物，把所有 agent-specific 转换职责完全下放给 agent 兼容层。
>
> 适用范围：cs-cloud / csc / costrict-web / app-ai-native。
>
> 状态：draft —— 待评审通过后落地。

---

## 1. 核心原则（Invariants）

| ID | 原则 | 含义 |
|----|------|------|
| **I1** | **路径即契约** | cs-cloud 对外的唯一产物是"宿主平台原生的绝对路径"。任何能 `open(path)` 的进程都能消费。 |
| **I2** | **Agent 无关** | 契约字段中**禁止**出现任何 cs-cloud / csc 专属语义：不约定 URL scheme 魔数、不约定 env var、不约定存储 layout。 |
| **I3** | **兼容层接管** | 从"路径"到"模型 API 请求体"的所有转换（base64、压缩、分块、格式适配）由 agent 兼容层独自负责，cs-cloud 不参与。 |
| **I4** | **文件优先消费** | agent 兼容层**必须**走其既有的多模态读取链路（csc 的情况下是 `FileReadTool`），不得绕过自行 `readFile` + base64。 |
| **I5** | **管理操作 vs 引用解耦** | 附件的 `id` 仅用于管理（删除、续期、查询、配额），**不进入 prompt 文本**。prompt 中只出现路径。 |

任何破坏以上任一条的"妥协设计"都视为契约违反，需要在评审时显式驳回。

---

## 2. 标准数据结构

### 2.1 cs-cloud 上传响应（v2）

```json
{
  "ok": true,
  "data": {
    "id": "01HW7N...",
    "abs_path": "/home/u/.costrict/cs-cloud/attachments/2026/07/08/01HW7N...png",
    "filename": "screenshot.png",
    "mime": "image/png",
    "size": 245678,
    "sha256": "9f86d...",
    "expires_at": "2026-07-15T08:00:00Z"
  }
}
```

字段约束：

| 字段 | 类型 | 用途 | 进入 prompt? |
|------|------|------|-------------|
| `id` | string (ULID) | 管理（DELETE / 查询 / 配额 / gc） | ❌ 禁止 |
| `abs_path` | string | **唯一对外引用句柄** | ✅ 唯一方式 |
| `filename` | string | UI 展示、审计 | ❌ |
| `mime` | string | UI 展示、agent 端可参考（不强制信任） | ❌（可作 metadata） |
| `size` | int64 | UI 展示、配额 | ❌ |
| `sha256` | string | 审计、去重、ETag | ❌ |
| `expires_at` | RFC3339 | UI 提示、agent 端可缓存 | ❌ |

**关键变化**：相比 v1 提案 §4.2.1，**不再返回 `key`**（v1 中的 `{hexID}/{filename}`）。`abs_path` 完全取代了 `key` 的角色。`id` 与 `abs_path` 是两个独立句柄，分别服务"管理"和"引用"。

### 2.2 prompt part —— 标准文件引用

prompt 中携带附件时使用**单一格式**，与用户 `@mention` 工作区文件完全一致：

```ts
type AttachmentPart = {
  type: "file"
  url: string  // 标准 file:// URI，见 §8
  mime?: string  // 可选提示，agent 端不强制信任
  filename?: string  // 可选展示字段
  // 元数据，用于 UI 回显与审计，agent 端忽略
  metadata?: {
    attachmentId: string  // cs-cloud 内部 id，不参与 prompt 文本
    source: "device-cache"
    expiresAt?: string
  }
}
```

**禁止**出现的字段（违反 I2）：

- ❌ `url: "file://attachments/{key}"` —— cs-cloud 专属 scheme
- ❌ `key: "..."` —— cs-cloud 内部 layout
- ❌ `attachmentId` 进入 prompt 文本（仅作为 part metadata）

---

## 3. cs-cloud 侧契约

### 3.1 端点行为

| 端点 | 行为 | 变化点 |
|------|------|--------|
| `POST /api/v1/attachments` | 上传，落盘，返回 §2.1 响应 | 响应增加 `abs_path`、`sha256`、`expires_at`；移除 `key` |
| `GET /api/v1/attachments/{id}` | 返回元数据（不含字节） | 新增 |
| `GET /api/v1/attachments/{id}/content` | 字节流，支持 Range | 新增 |
| `DELETE /api/v1/attachments/{id}` | 软删除 | 新增 |
| `POST /api/v1/attachments:gc` | 触发 gc | 新增 |
| `GET /api/v1/attachments` | 列表，支持 `?session_id=` 等 | 新增 |

**保留**当前 stash 已有的 4 个端点作为兼容入口，但**响应字段升级**到 v2。

### 3.2 路径生成规则（强制）

1. `abs_path` 必须是**当前平台原生格式**：
   - Unix：`/home/u/.costrict/cs-cloud/attachments/2026/07/08/{ulid}.png`
   - Windows：`C:\Users\u\.costrict\cs-cloud\attachments\2026\07\08\{ulid}.png`
2. 由 `internal/platform/paths.go` 的 `Join` 保证分隔符正确，禁止手写字符串拼接。
3. **文件名一律使用 ULID**，不使用客户端上传的 `filename`，避免：
   - 路径穿越（`../`）
   - 编码问题（Unicode、emoji）
   - 文件名冲突
4. 客户端上传的 `filename` 仅存入 `meta.json` 作为展示字段。

### 3.3 路径穿越防御（强制）

写入与读取路径必须执行：

```go
absPath := filepath.Join(attachmentsRoot, ulid + ext)
cleaned := filepath.Clean(absPath)
if !strings.HasPrefix(cleaned, filepath.Clean(attachmentsRoot) + string(os.PathSeparator)) {
    return ErrPathEscape
}
// 同时拒绝 symlink
info, err := os.Lstat(cleaned)
if err != nil { return err }
if info.Mode()&os.ModeSymlink != 0 {
    return ErrSymlink
}
```

### 3.4 生命周期

- TTL：默认 7 天，可配置上限 30 天
- 软删 → `.trash/` 保留 24h → 物理删除
- gc 触发：定时 6h / `POST :gc` / 配额超 hard_limit

**csc 端不允许出现 `CSC_ATTACHMENTS_DIR` 或任何 cs-cloud 命名空间的环境变量。** cs-cloud 不向 agent 进程注入任何环境变量（v1 stash 中的 `CSC_ATTACHMENTS_DIR` 注入要删除）。

---

## 4. csc 兼容层契约

### 4.1 输入期望

csc 收到的 prompt part `url` 必须是**标准 `file://` URI 或绝对路径**：

```ts
// 合法
url: "file:///home/u/.costrict/cs-cloud/attachments/.../01HW7N.png"
url: "/home/u/.costrict/cs-cloud/attachments/.../01HW7N.png"
url: "file:///C:/Users/u/.costrict/cs-cloud/attachments/.../01HW7N.png"
```

**禁止**期望以下输入（当前实现违反，需要返工）：

```ts
url: "file://attachments/{key}"  // ❌ cs-cloud 专属 scheme
env.CSC_ATTACHMENTS_DIR          // ❌ cs-cloud 专属 env
```

### 4.2 转换职责（强制 I3 + I4）

csc 收到 `file://` 引用后：

1. 用 Node.js 标准 `fileURLToPath(url)` 解析为本地路径，**不读任何 cs-cloud env**。
2. 路径穿越校验：对解析后的路径 `filepath.Clean` 后判断是否合法（不需要知道 cs-cloud layout，只判断是否绝对路径、是否可读）。
3. **走 `FileReadTool`** 消费，**不得**直接 `readFile(path, 'base64')`：
   - 让 `imageResizer.ts` 自适应压缩生效
   - 让 PDF 分页、格式校验生效
   - 让 `API_IMAGE_MAX_BASE64_SIZE` 上限保护生效
4. FileReadTool 输出的多模态结构由 csc 内部按当前 model 适配：
   - Anthropic：`ContentBlockParam { type: 'image', source: { type: 'base64', ... } }`
   - OpenAI：`{ type: 'image_url', image_url: { url: '...' } }`
   - Gemini：`{ inlineData: { ... } }`
   - 未来换 agent 时只改这一段

### 4.3 当前实现需要返工的具体点（commit `b9fd73238`）

| 位置 | 现状 | 调整方向 |
|------|------|---------|
| `src/server/sessionHandle.ts:80` | `env.CSC_ATTACHMENTS_DIR` | **删除**，路径直接来自 url |
| `src/server/sessionHandle.ts:103` | `/^file:\/\/attachments\/(.+)$/` | 改为标准 `fileURLToPath(url)` |
| `src/server/sessionHandle.ts:106` | `join(attachmentsDir, key)` | 改为 `fileURLToPath(url)` |
| `src/server/sessionHandle.ts:108` | `readFile(filePath, 'base64')` | 改为调用 `FileReadTool` 或其内部等价物 |
| `src/server/sessionHandle.ts:111-114` | 硬编码 Anthropic ContentBlock | 改为按当前 model provider 分派（提取到单独适配文件） |

返工后单测 `sessionHandle.test.ts` 中相关 mock 要同步调整。

---

## 5. UI 侧契约（app-ai-native）

### 5.1 上传流程

新增 `src/client/attachment-client.ts`：

```ts
export type UploadedAttachment = {
  id: string
  absPath: string
  filename: string
  mime: string
  size: number
  sha256: string
  expiresAt: string
}

export async function uploadAttachment(
  file: File,
  opts: { deviceId: string; sessionId?: string }
): Promise<UploadedAttachment> {
  const form = new FormData()
  form.append('file', file)
  if (opts.sessionId) form.append('session_id', opts.sessionId)
  const res = await fetch(`/api/devices/${opts.deviceId}/attachments`, {
    method: 'POST',
    body: form,
  })
  if (!res.ok) throw new DeviceHttpError(res)
  const json = await res.json()
  return {
    id: json.data.id,
    absPath: json.data.abs_path,
    filename: json.data.filename,
    mime: json.data.mime,
    size: json.data.size,
    sha256: json.data.sha256,
    expiresAt: json.data.expires_at,
  }
}
```

### 5.2 prompt 构造（替换 `build-request-parts.ts:175-183`）

```ts
const images = input.images.map((att) => ({
  id: Identifier.ascending("part"),
  type: "file" as const,
  mime: att.mime,
  url: `file://${att.absPath}`,  // ★ 标准文件 URI，与 @mention 走同一通路
  filename: att.filename,
  metadata: {
    attachmentId: att.attachmentId,
    source: "device-cache" as const,
    expiresAt: att.expiresAt,
  },
}))
```

### 5.3 `attachments.ts` 改造

`addImageAttachment` 不再 `FileReader.readAsDataURL`：

```ts
const addImageAttachment = async (file: File) => {
  if (!ACCEPTED_FILE_TYPES.includes(file.type)) return
  try {
    const result = await uploadAttachment(file, { deviceId, sessionId })
    const attachment: ImageAttachmentPart = {
      type: "image",
      id: uuid(),
      attachmentId: result.id,
      filename: result.filename,
      mime: result.mime,
      absPath: result.absPath,      // ★
      expiresAt: result.expiresAt,
    }
    prompt.set([...prompt.current(), attachment], cursorPosition)
  } catch (err) {
    // 失败回退 base64 内联（仅作为兜底，长期目标删除）
    fallbackToBase64(file)
  }
}
```

### 5.4 失败回退策略

- 设备不支持（404/501 from cs-cloud）→ 回退 base64 内联
- 网络错误 → 重试 3 次后回退
- 尺寸超限（413）→ **不**回退，提示用户

回退路径需在 UI 上显示标记（"附件未上传到设备"），避免无声降级。

---

## 6. costrict-web 侧契约

### 6.1 路由决策

**方案 A（推荐 v1）**：复用通用 proxy `/cloud/device/:deviceID/proxy/*path`，不新增专用路由。

- 优点：零 server 改动
- 缺点：无专用鉴权 / 限流 / 审计；30s 超时（`proxy_handler.go:54`）对慢上传可能不够

**方案 B（推荐 v2）**：新增 `POST /api/devices/:deviceID/attachments`，叠加：
- 显式尺寸限制（`http.MaxBytesReader`，按 mime 区分）
- 30s → 300s 超时
- 结构化审计日志（user_id + device_id + attachment_id）
- 独立限流（按 user 维度）

**建议**：v1 走方案 A 跑通端到端；v2 视压测结果决定是否升级到方案 B。

### 6.2 gateway 改进项（v2）

`gateway/internal/proxy_handler.go:70` 的 `io.ReadAll(c.Request.Body)` 全量入内存，需要改为流式：

```go
// 现状
bodyBytes, err := io.ReadAll(c.Request.Body)

// 改进方向：流式写入 stream，配合 Content-Length 分块
// （需要 csc 端 tunnel 也支持流式读 body，参考 proxy.go 的 io.CopyN 模式）
```

此为 §10 "Phase 2" 工作项，不阻塞 v1 上线。

---

## 7. 跨平台路径（强制 I1）

### 7.1 `file://` URI 规范

- Unix：`file:///abs/path` （三斜杠）
- Windows：`file:///C:/Users/u/...` （三斜杠 + 盘符 + 正斜杠）
- 所有反斜杠在 URI 中必须转为正斜杠

UI 构造时使用：

```ts
function toFileUri(absPath: string): string {
  // 跨平台转换：Windows 的 C:\Users → file:///C:/Users
  const normalized = absPath.replace(/\\/g, '/')
  return normalized.startsWith('/') 
    ? `file://${normalized}`  // Unix: file:///abs
    : `file:///${normalized}` // Windows: file:///C:/...
}
```

cs-cloud 端则在响应中提供**平台原生格式**的 `abs_path`（Windows 用反斜杠，Unix 用正斜杠），由 UI 端负责 URI 化。

### 7.2 Windows 长路径

缓存根 ≤ 80 字符，年月日分桶 + ULID 总长可控（通常 < 200 字符）。如触发 >260 限制：

- 优先缩短缓存根（`SetDataDir`）
- 兜底启用 `\\?\` 前缀（仅 Windows，cs-cloud 内部处理，不暴露给 agent）

### 7.3 路径风格一致性

`index.json` 内**统一用 POSIX 风格**存储（便于跨设备同步与排查），展示时由 cs-cloud 按当前平台转换。

---

## 8. 安全基线

### 8.1 客户端不可信

- `Content-Type` 不可信：cs-cloud 落盘后用 `gabriel-vasile/mimetype` 二次嗅探
- `filename` 不可信：仅作为展示字段，不进入落盘路径
- 客户端提供的 `mime` 在响应中可被覆盖（嗅探后的真实 mime）

### 8.2 路径穿越

见 §3.3。

### 8.3 配额

| 维度 | 默认 | 来源 |
|------|------|------|
| 单文件 | 10MB（图片）/ 50MB（其他） | 按 mime 区分 |
| 单请求 | 50MB | `tunnel/proxy.go:maxBodySize` |
| 设备软上限 | 1GB | `attachments.soft_limit` |
| 设备硬上限 | 2GB | `attachments.hard_limit` |

### 8.4 MIME 白名单

默认：`image/*`、`application/pdf`、`text/*`、`application/zip`。

**白名单只在校验入口生效**。一旦文件落盘，agent 端读到的是任意路径，因此 agent 端也必须有"按扩展名/magic bytes 决定是否处理"的自我保护（csc FileReadTool 已具备）。

---

## 9. 迁移路径（从当前 v1 状态）

当前各端状态：

| 仓库 | 当前状态 | 距 v2 还差 |
|------|---------|-----------|
| cs-cloud（stash@{1}） | 4 端点 + 24h TTL + env 注入 | §3 全部：abs_path/sha256/ULID/分桶/路径穿越防御/MIME 嗅探/删除 env 注入 |
| csc（commit b9fd73238） | `file://attachments/{key}` + env | §4.3 全部：删除 env、改 URI 解析、走 FileReadTool、provider 分派 |
| costrict-web | 无 | §6 决策（v1 走方案 A 则零改动） |
| app-ai-native | base64 内联 | §5 全部 |

**推荐落地顺序**：

1. **先调整契约面**（不改运行时行为）
   - cs-cloud：上传响应增加 `abs_path`、`sha256`，保留 `key` 字段做兼容（标 deprecated）
   - csc：兼容层同时识别 `file://attachments/{key}`（旧）和 `file://` 标准路径（新）

2. **UI 切换到新契约**
   - 实现 `attachment-client.ts`
   - `attachments.ts` 改走 upload，失败回退 base64
   - `build-request-parts.ts` 用 `file://${absPath}` 构造 part

3. **端到端验证**
   - UI 上传 → cs-cloud 落盘 → csc 读到 abs_path → FileReadTool → Anthropic 块

4. **清理废弃字段**
   - csc 删除 `file://attachments/{key}` 兼容分支与 `CSC_ATTACHMENTS_DIR` env 读取
   - cs-cloud 删除 stash 中的 env 注入逻辑（`daemon.go`、`serve.go`）
   - cs-cloud 上传响应删除 `key` 字段
   - cs-cloud 落盘 layout 切到 `年/月/日/{ulid}`

5. **加固**
   - 路径穿越、MIME 嗅探、配额、gc

每一步都可独立合入 main，不需要 big-bang。

---

## 10. 开放问题

1. **session 级 gc 触发**：csc 会话结束时是否回调 cs-cloud 触发该会话附件 gc？建议 v1 不做，依赖 `expires_at` 统一管控。
2. **缩略图**：UI 回显缩略图当前依赖原文件（`GET /{id}/content`）。如性能不达标，v2 增加 `?size=thumb`。
3. **跨设备共享**：附件从 A 设备移到 B 设备是否需要云端中转？v1 不做，全部本地。
4. **csc 是否需要在 prompt 文本里也保留 `@{abs_path}` 引用**？目前 part.url 已经够用，但 LLM 在思考时可能更倾向于引用文本。建议**附加**：UI 在上传成功后，**同时**把 `@{abs_path}` 作为文本片段插入编辑器，与 part.url 双保险。这样 LLM 即使在不支持多模态的上下文里也能"看到"引用。
5. **多模态 provider 适配层在 csc 内部的位置**：建议新增 `src/services/agent-compat/attachments.ts`，把 §4.2 第 4 步的"按 provider 分派"集中在一个文件，避免散落在 sessionHandle.ts。

---

## 11. 契约校验清单（评审用）

任何 PR 声称"实现 v2 契约"时，必须能回答以下全部 ✅：

- [ ] cs-cloud 上传响应包含 `abs_path`、`sha256`、`expires_at`，不包含 `key`
- [ ] cs-cloud 不向 agent 进程注入任何 cs-cloud 命名空间的 env var
- [ ] cs-cloud 落盘路径与目录 layout（年月日分桶）不出现在任何外部契约字段
- [ ] cs-cloud 写入与读取都有路径穿越 + symlink 防御
- [ ] csc 不读取 `CSC_ATTACHMENTS_DIR` 或任何 cs-cloud 命名空间 env
- [ ] csc 用 `fileURLToPath` 解析路径，不用魔数正则
- [ ] csc 通过 `FileReadTool` 消费附件，不直接 `readFile`
- [ ] csc 的 Anthropic/OpenAI/Gemini 转换逻辑可独立替换
- [ ] UI 上传失败时显式标记降级，不静默回退
- [ ] UI 构造的 `part.url` 是标准 `file://` URI，可被任意 file-URI-aware 工具解析
- [ ] prompt 文本中不出现 `attachmentId`、`key` 等 cs-cloud 内部标识符

---

## 附录 A：契约层反例对照

| 场景 | ❌ v1（当前） | ✅ v2 |
|------|--------------|-------|
| cs-cloud → csc 引用 | `file://attachments/abc123/screenshot.png` | `file:///home/u/.costrict/.../abc123.png` |
| csc 知道目录在哪 | `process.env.CSC_ATTACHMENTS_DIR` | URL 自带路径 |
| 换 agent | 新 agent 也要学 `file://attachments/` 与 env | 新 agent 用标准库 `fileURLToPath` 即可 |
| 文件布局变更 | csc 跟着改 | csc 完全无感知 |
| 排错 | `ls ~/.costrict/cs-cloud/attachments/abc123/` | `ls /abs/path/from/url` |
| 审计 | 需要解释 `key` 的含义 | 路径即审计键 |
