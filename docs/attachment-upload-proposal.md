# 附件上传（图片→设备缓存→Agent 读取）技术提案

> 目标：让 Web 控制台（`app-ai-native`）用户能把图片等附件上传到目标设备的本地缓存目录，再以**文件路径**形式注入 `csc` Agent，由 Agent 自身的 FileReadTool 完成读取与多模态识别。
>
> 相关仓库：
> - UI：`D:\DEV\opencode\packages\app-ai-native`
> - Server + Gateway：`D:\DEV\costrict-web`
> - 桥接层（本文档所在仓库）：`D:\DEV\cs-cloud`
> - Agent：`D:\DEV\csc`

---

## 1. 项目现状

### 1.1 已有能力

| 能力 | 位置 | 状态 | 备注 |
|------|------|------|------|
| WebSocket + yamux 隧道（二进制帧） | `internal/tunnel/connect.go` | 可用 | `MaxStreamWindowSize=4MB`，单流累计可超 |
| 隧道反向代理（含大 body 流式） | `internal/tunnel/proxy.go` | 可用 | **`maxBodySize = 50MB`**（`proxy.go:13`），`>1MB` 走 `io.CopyN` 流式 |
| 本地 HTTP Control Plane | `internal/localserver/` | 可用 | 仅监听 `127.0.0.1`，路由集中注册 |
| 文件读 / 写 / 列表 / 模糊查找 | `internal/localserver/runtime_file*.go` / `find_files.go` | 可用 | **写入仅支持文本**（`runtime_files.go:318-320` 拒绝二进制） |
| 工作区沙箱 | `internal/localserver/response.go:56-63` | 可用 | 通过 `X-Workspace-Directory` 头约束 |
| 平台路径抽象 | `internal/platform/paths.go` | 可用 | `AppDir()=~/.costrict/cs-cloud/`，可 `SetDataDir` 覆盖 |
| Gateway 转发（`/device/:id/proxy/*`） | `costrict-web/gateway/internal/proxy_handler.go` | 可用 | `io.ReadAll` 全量读入内存，**未设显式上限** |
| Server 端 multipart 上传（artifacts） | `costrict-web/server/internal/handlers/artifact.go` | 可用 | `POST /artifacts/upload`，使用 `c.Request.FormFile` |
| Server 端尺寸约束 | `costrict-web/server/internal/services/archive_service.go:17,19` | 可用 | 归档 50MB / 单文件 10MB，`http.MaxBytesReader` |
| 本地存储后端 | `costrict-web/server/internal/storage/` | 可用 | 仅 `LocalBackend`，无 S3/OSS |
| CSC 图片读取 | `csc/packages/builtin-tools/src/tools/FileReadTool/` | 可用 | PNG/JPEG/GIF/WebP，自带压缩与 base64 编码 |
| CSC `@mention` 文件引用 | `csc/src/utils/attachments.ts` | 可用 | `@/abs/path/to.png` 自动读取 |
| CSC ACP 协议附件 | `csc/src/services/acp/` | 可用 | `PromptRequest` 结构化附件 |
| UI 图片粘贴 / 拖拽 / 选择 | `app-ai-native/src/components/prompt-input/attachments.ts` | 可用 | 当前以 base64 dataUrl 内联 |
| UI 渲染 markdown 图片 | `app-ai-native/src/styles/session-markdown.css` + MarkedProvider | 可用 | 标准 `![](url)` |

### 1.2 当前图片传输的"隐式通路"

UI 侧已经具备完整的图片采集能力（粘贴 / 拖拽 / 文件选择），但**目前没有走 cs-cloud 缓存路径**，而是把图片转成 base64 dataUrl 后直接塞进 `prompt.parts[].url`：

```
app-ai-native
   │  attachments.ts → FileReader.readAsDataURL
   │  build-request-parts.ts → { type:"file", url:"data:image/png;base64,..." }
   ▼
/prompt/async → costrict-web gateway → cs-cloud tunnel → csc
   │
   ▼
csc 把 base64 内联进 LLM 消息（Anthropic BetaImageBlockParam 等）
```

这条通路对 ≤1MB 的截图够用，但存在以下问题（详见 §2.2）：

1. **重复传输**：同一张图被 UI→Gateway→cs-cloud→csc→模型逐段拷贝，每段都全量驻留内存。
2. **会话体积膨胀**：base64 让原体积膨胀 ~33%，且会进入会话历史 / 数据库 / 重放链路。
3. **无法复用**：Agent 工具（如 FileReadTool）只能拿到 base64，无法走"路径读取→按需缩放→分块"优化路径。
4. **超限风险**：`csc/src/utils/imageResizer.ts` 的 `API_IMAGE_MAX_BASE64_SIZE` 会硬性拒绝过大的 base64。
5. **审计与可追溯性差**：图片没有"实物"，只在内存里过一遍。

### 1.3 关键缺口

| # | 缺口 | 影响 |
|---|------|------|
| 1 | cs-cloud localserver 无 multipart / 二进制写入端点 | 无法把字节流落盘到设备 |
| 2 | 无附件缓存目录约定与生命周期管理 | 没有放置上传文件的"家" |
| 3 | localserver 现有文件写入拒绝二进制 | 不能复用 `PUT /runtime/files/content` |
| 4 | 无"附件 ID ↔ 物理路径"映射与查询接口 | csc 端难以稳定引用 |
| 5 | costrict-web 无"转发到设备缓存"的路由 | gateway 已具备能力，但缺上层封装 |
| 6 | UI 仍走 base64 内联 | 即使后端就绪，前端不变也用不上 |
| 7 | 无统一尺寸 / 类型 / 病毒扫描策略 | 大文件 / 异常 MIME 会击穿隧道 |

---

## 2. 设计目标与非目标

### 2.1 目标

1. **G1 通用附件**：第一版以图片为主，但 API/存储/生命周期不绑定到"图片"，未来可承载 PDF、录音、zip 等任意附件。
2. **G2 路径优先**：UI 默认不再走 base64 内联，而是上传到设备缓存后以**绝对路径**作为引用传入 Agent。
3. **G3 端到端可见**：每张附件都有稳定 ID、可查询的元数据（mime/size/created_at/sha256）、可被 UI 回显缩略图。
4. **G4 安全沙箱**：附件落盘严格限定在缓存根目录内，禁止路径穿越；上传与读取均经隧道鉴权。
5. **G5 跨平台一致**：Linux / Windows / macOS 行为一致；返回给 csc 的路径需符合宿主规范。
6. **G6 流式与限速**：≤50MB 走现成隧道；超大附件提供 chunked 续传方案（v2）。
7. **G7 可运维**：缓存目录有上限、有保留期、有清理策略；提供 `cs-cloud` CLI 查询与回收。

### 2.2 非目标

- **N1 不做云端对象存储**（S3/OSS）。附件只在设备本地，云端不留存原始字节。
- **N2 不替代 artifact 上传**。`costrict-web` 既有的 `POST /artifacts/upload` 面向平台资产（capability 包等），与本文档的"会话级临时附件"语义不同。
- **N3 不修改 csc 内部的图片处理流水线**。FileReadTool 已经覆盖 PNG/JPEG/GIF/WebP/PDF，本提案只负责"把字节送进路径"。
- **N4 第一版不做端到端加密**。隧道层已有 TLS + device_token，附件不再叠加应用层加密。

---

## 3. 总体架构

### 3.1 端到端数据流

```
┌────────────────────────────────────────────────────────────────┐
│ 浏览器（app-ai-native，SolidJS）                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ prompt-input/attachments.ts                              │  │
│  │  - paste / drop / file picker                            │  │
│  │  - 不再 FileReader.readAsDataURL                          │  │
│  │  - 改为：先调 uploadAttachment(file) → 拿到 attachment_id  │  │
│  └──────────────────────┬───────────────────────────────────┘  │
│                         │ multipart/form-data                    │
└─────────────────────────┼────────────────────────────────────────┘
                          │ HTTPS
┌─────────────────────────┼────────────────────────────────────────┐
│  costrict-web server   │                                        │
│  ┌─────────────────────▼────────────────────────────────────┐    │
│  │ POST /api/devices/{id}/attachments            (NEW)       │    │
│  │  - 鉴权（JWT + workspace 权限）                              │  │
│  │  - 限制尺寸/类型（http.MaxBytesReader）                    │  │
│  │  - 透传 multipart body 到 gateway                          │  │
│  └─────────────────────┬────────────────────────────────────┘    │
└────────────────────────┼─────────────────────────────────────────┘
                         │ InternalSecret
┌─────────────────────────┼────────────────────────────────────────┐
│  costrict-web gateway  │                                        │
│  ┌─────────────────────▼────────────────────────────────────┐    │
│  │ ANY /device/{id}/proxy/v1/attachments          (existing)│    │
│  │  - 走现成的 yamux 反向代理                                  │  │
│  │  - body 经 io.ReadAll → 流式写设备                          │  │
│  └─────────────────────┬────────────────────────────────────┘    │
└────────────────────────┼─────────────────────────────────────────┘
                         │ WebSocket + yamux（≤50MB）
┌─────────────────────────┼────────────────────────────────────────┐
│  cs-cloud daemon       │                                        │
│  ┌─────────────────────▼────────────────────────────────────┐    │
│  │ localserver                                               │    │
│  │  POST /api/v1/attachments                       (NEW)     │    │
│  │   ├─ multipart reader → io.TeeReader → 落盘                │  │
│  │   ├─ 计算 sha256 / size / mime                            │  │
│  │   ├─ 写元数据 index.json + 自增 ID                         │  │
│  │   └─ 返回 { id, path, size, mime, sha256 }                │  │
│  │                                                          │  │
│  │  GET  /api/v1/attachments/{id}                  (NEW)     │  │
│  │  GET  /api/v1/attachments/{id}/content          (NEW)     │  │
│  │  DELETE /api/v1/attachments/{id}                (NEW)     │  │
│  │  POST /api/v1/attachments:gc                    (NEW)     │  │
│  └─────────────────────┬─────────────────────────────────────┘  │
│                        │                                          │
│         ┌──────────────▼──────────────────┐                      │
│         │ attachments/  (~/.costrict/cs-cloud/attachments)       │
│         │  ├─ index.json                                           │
│         │  ├─ 2026/07/06/{ulid}.png                                │
│         │  └─ 2026/07/06/{ulid}.pdf                                │
│         └─────────────────────────────────────────────┘           │
└──────────────────────────────────────────────────────────────────┘
                         │ 提示词中以 @/abs/path 引用
                         ▼
                       csc agent（FileReadTool）
```

### 3.2 与现有"隐式通路"的对照

| 维度 | 旧：base64 内联 | 新：设备缓存 + 路径 |
|------|----------------|-------------------|
| UI→Gateway 载荷 | base64（×1.33） | multipart 原始字节 |
| Gateway 内存峰值 | 全量驻留 | 流式转发后释放 |
| csc 拿到的形态 | base64 字符串 | 文件路径，由 FileReadTool 处理 |
| 复用 | 不可 | 同一 ID 可多次引用 |
| 会话历史 | 含完整 base64 | 仅含路径字符串 |
| 审计/追溯 | 无 | 有 ID + sha256 + 元数据 |
| 失败重试 | 整段重传 | 仅重传失败分片（v2） |

---

## 4. 详细设计

### 4.1 缓存目录布局

```
~/.costrict/cs-cloud/attachments/         ← 平台无关根（platform.AppDir 子目录）
├── index.json                            ← 附件元数据索引（可被并发追加）
├── 2026/                                  ← 按年/月/日分桶（避免单目录爆炸）
│   └── 07/
│       └── 06/
│           ├── 01HW7N...AB.png           ← 文件名 = ULID（自带时序）
│           ├── 01HW7N...AB.meta.json     ← 单文件元数据（冗余 + 易备份）
│           └── 01HW7N...CD.pdf
└── .trash/                                ← 软删除回收站，gc 周期清理
```

**选型说明**：

- **ULID 而非 UUID**：自带毫秒级时间戳，天然按时间排序，便于按时间段清理。
- **年/月/日分桶**：单目录文件数控制在 ~10k 量级，避免 Windows NTFS 长路径与目录枚举开销。
- **`.meta.json` 冗余**：即便 `index.json` 损坏，仍可从单个 `.meta.json` 重建索引。
- **`.trash/` 软删**：避免"用户误删→立刻被 Agent 引用"的竞态；保留一个 gc 周期再物理删除。

`index.json` 结构（每个 entry）：

```json
{
  "id": "01HW7N...",
  "ulid": "01HW7N...",
  "filename": "screenshot.png",
  "mime": "image/png",
  "size": 245678,
  "sha256": "9f86d...",
  "abs_path": "/home/u/.costrict/cs-cloud/attachments/2026/07/06/01HW7N...AB.png",
  "workspace": "/home/u/projects/foo",
  "session_id": "sess_abc",
  "created_at": "2026-07-06T08:00:00Z",
  "expires_at": "2026-07-13T08:00:00Z",
  "deleted_at": null
}
```

### 4.2 cs-cloud localserver 端点契约

所有端点都遵循现有 `internal/localserver/router.go` 的注册风格，配 Swagger 注解。

#### 4.2.1 `POST /api/v1/attachments` —— 上传

**请求**：`multipart/form-data`

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `file` | binary | ✅ | 附件字节流 |
| `filename` | text | | 原始文件名（仅用于展示，不影响落盘名） |
| `workspace` | text | | 工作区绝对路径，用于权限校验与命名空间 |
| `session_id` | text | | 关联会话，便于按会话清理 |
| `ttl_seconds` | text | | 保留时长，默认 7 天，上限 30 天 |

**响应**（`201 Created`）：

```json
{
  "ok": true,
  "data": {
    "id": "01HW7N...",
    "filename": "screenshot.png",
    "mime": "image/png",
    "size": 245678,
    "sha256": "9f86d...",
    "abs_path": "/home/u/.costrict/cs-cloud/attachments/2026/07/06/01HW7N...AB.png",
    "expires_at": "2026-07-13T08:00:00Z"
  }
}
```

**实现要点**：

1. **流式落盘**：用 `multipart.Reader.ReadForm` + `io.TeeReader`，边读边写盘 + 算 sha256，避免全量入内存（与 `proxy.go` 的 `io.CopyN` 风格一致）。
2. **MIME 嗅探**：客户端上传的 `Content-Type` 不可信，落盘后用 `gabriel-vasile/mimetype` 做服务端二次嗅探（读前 512 字节即可），与白名单对照，不匹配直接拒绝并清理临时文件。
3. **路径生成**：`abs_path` 由 `platform.AppDir()` 派生，**不接受客户端路径参数**。
4. **配额**：单设备全局软上限（默认 1GB），超出则拒绝新上传并提示用户运行 `cs-cloud attachments prune`。

#### 4.2.2 `GET /api/v1/attachments/{id}` —— 元数据

返回 `index.json` 中对应 entry（不含二进制）。供 UI 回显缩略图、显示尺寸/类型。

#### 4.2.3 `GET /api/v1/attachments/{id}/content` —— 二进制内容

- 加 `Range` 支持，方便大文件分片读取（图片用不上，但 PDF/zip 需要）。
- 默认 `Content-Disposition: inline`；带 `?download=1` 时改为 `attachment`。
- 自动设置 `Content-Type` 与 `ETag`（用 sha256）。

#### 4.2.4 `DELETE /api/v1/attachments/{id}` —— 软删除

把 entry 移到 `.trash/`，设置 `deleted_at`。物理删除由 gc 任务统一处理。

#### 4.2.5 `POST /api/v1/attachments:gc` —— 清理

触发条件：

- `expires_at` 已过；
- `deleted_at` 超过 24h；
- 全局配额超过 `hard_limit`（默认 2GB），按 LRU 淘汰。

#### 4.2.6 `GET /api/v1/attachments` —— 列表

支持 `?session_id=`、`?workspace=`、`?limit=`、`?before=` 分页。

### 4.3 costrict-web server 端点

#### `POST /api/devices/{device_id}/attachments`

**职责**：

1. JWT 鉴权（沿用现有 `authMW`）；
2. 校验当前用户对 `{device_id}` 的访问权限（沿用 `session_service.go` 中的 workspace 解析）；
3. 校验尺寸 / 类型（`http.MaxBytesReader`，复用 `archive_service.go` 的 50MB / 10MB 阈值）；
4. 透传 multipart 到 `/device/{device_id}/proxy/v1/attachments`（走现有 `gateway/client.go:ProxyRequest`）；
5. 把设备返回的 `abs_path` 透回给前端。

**为什么不让前端直连 gateway**：保持"所有外部入口都经 server"的现有边界（见 `docs/api-surface-separation.md` 思路），便于统一审计与限流。

### 4.4 UI 改造（`app-ai-native`）

新增 `src/client/attachment-client.ts`：

```ts
export async function uploadAttachment(
  file: File,
  opts: { deviceId: string; workspace?: string; sessionId?: string }
): Promise<{ id: string; absPath: string; mime: string; size: number; sha256: string }> {
  const form = new FormData()
  form.append('file', file)
  if (opts.workspace) form.append('workspace', opts.workspace)
  if (opts.sessionId) form.append('session_id', opts.sessionId)
  const res = await fetch(`/api/devices/${opts.deviceId}/attachments`, {
    method: 'POST',
    body: form,
  })
  if (!res.ok) throw new DeviceHttpError(res)
  return (await res.json()).data
}
```

`prompt-input/attachments.ts` 改造点：

1. **不再走** `FileReader.readAsDataURL`，改为调用 `uploadAttachment`；
2. 上传期间显示进度（`XMLHttpRequest.upload.onprogress`，fetch 的 `ReadableStream` 替代方案）；
3. 上传成功后把 `attachment_id` 与 `abs_path` 一并存入 `AttachmentPart` 状态；
4. `build-request-parts.ts` 把图片 part 改成两种形态之一：
   - **路径形态**（默认）：`{ type: 'file', source: 'device-cache', path: absPath, attachmentId }`
   - **降级形态**（设备不支持时）：原 base64 dataUrl。
5. 兼容旧前端的判断：第一次 `uploadAttachment` 收到 404/501 时，自动回退到 base64。

### 4.5 csc 接入方式

**零改动**。Agent 接收到的 prompt 文本里会包含 `@{abs_path}`，csc 既有的 `@mention` 流程会自动调用 `FileReadTool`：

- `csc/packages/builtin-tools/src/tools/FileReadTool/FileReadTool.ts` 已支持 PNG/JPEG/GIF/WebP/PDF；
- `csc/src/utils/imageResizer.ts` 会按 `API_IMAGE_MAX_BASE64_SIZE`、`IMAGE_MAX_WIDTH/HEIGHT` 自适应压缩；
- 因为读到的是真实文件而非 base64 字符串，**所有现有的按需缩放 / 分块 / 缓存策略都能生效**。

**对 ACP 协议的影响**：`csc/src/services/acp/agent.ts` 中 `PromptRequest` 已支持附件，cs-cloud 在派发命令时把 `attachment_id` 与 `abs_path` 写入 prompt 文本即可，无需扩展协议字段。

---

## 5. 跨切关注点

### 5.1 鉴权与权限

| 层 | 既有机制 | 复用情况 |
|----|---------|---------|
| UI → server | Casdoor JWT | ✅ 不变 |
| server → gateway | InternalSecret | ✅ 不变 |
| gateway → cs-cloud | device_token（隧道握手时校验） | ✅ 不变 |
| cs-cloud localserver | 隧道天然可信（仅 127.0.0.1） | ✅ 不变 |
| 工作区权限 | server 端 `session_service` 解析 | ✅ 复用 |

**新增**：附件落盘时把 `workspace` 写入 `index.json`，将来若要做"会话级隔离"，可在 `GET /attachments` 上叠加 workspace 过滤。

### 5.2 路径安全

1. **不接受客户端指定路径**：`abs_path` 完全由服务端根据 `platform.AppDir()` + ULID 派生。
2. **目录穿越防护**：所有读取接口都先用 `filepath.Sanitize` + 校验结果仍在 `attachments/` 之下，参考 `runtime_files.go:resolvePath` 现有套路。
3. **符号链接拒绝**：落盘前 `Lstat` 检查目标目录不是 symlink。

### 5.3 尺寸与类型限制

| 维度 | 默认值 | 来源 |
|------|-------|------|
| 单文件上限 | 10MB（图片）/ 50MB（其他） | 复用 `archive_service.go` 阈值，按 mime 区分 |
| 单次请求上限 | 50MB | `tunnel/proxy.go:maxBodySize` |
| 设备全局软上限 | 1GB | 新增配置 `attachments.soft_limit` |
| 设备全局硬上限 | 2GB | 新增配置 `attachments.hard_limit` |
| 允许的 MIME 白名单 | image/*、application/pdf、text/*、application/zip | 配置可覆盖 |
| 文件名长度 | ≤ 255 字符 | 落盘前截断 |

**超限行为**：

- 单文件超限 → 413；
- 设备配额超限 → 507，提示用户运行 `cs-cloud attachments prune`；
- 类型不在白名单 → 415。

### 5.4 跨平台路径

- 返回给 UI / csc 的 `abs_path` 必须是**当前平台原生格式**（Windows 用 `\`，Unix 用 `/`），由 `internal/platform/paths.go` 的 `Join` 保证。
- `index.json` 内**统一用 POSIX 风格**存储（便于跨设备同步与排查），展示时再转换。
- Windows 长路径（>260 字符）风险：缓存根 ≤ 80 字符，年月日分桶 + ULID 总长可控，通常不会触发；如触发则启用 `\\?\` 前缀（仅 Windows）。

### 5.5 生命周期与清理

| 状态 | 触发 | 动作 |
|------|------|------|
| active | 上传成功 | 可被读取 |
| expired | `expires_at` 到期 | gc 任务移到 `.trash/` |
| trashed | 用户 `DELETE` | 移到 `.trash/`，保留 24h |
| purged | trash 超 24h 或 hard_limit 触发 | 物理删除 |

gc 任务由 `cs-cloud` daemon 内置（参考 `internal/updater` 的 ticker 模式），默认每 6h 跑一次，可在配置中关闭。

### 5.6 可观测性

- 每次上传/下载/删除打一条结构化日志（zap），字段：`attachment_id`、`device_id`、`session_id`、`size`、`mime`、`duration_ms`。
- 上传失败、配额触发、gc 触发均走 WARN 级别。
- `cs-cloud status` 增加 `attachments.count` / `attachments.total_size` / `attachments.oldest` 三项指标。
- 未来如需接入云端监控，可把 `index.json` 的元数据（不含字节）按 heartbeat 上报。

### 5.7 并发与一致性

- `index.json` 写入用 `os.FileLock`（跨平台 `github.com/gofrs/flock`），避免并发追加竞态。
- 落盘顺序：先写临时文件 `.tmp.{ulid}` → `fsync` → `rename` 为最终名 → 再写 `index.json`。
- 删除时先软删 `index.json`，再延迟物理删，确保读取侧看到的元数据始终一致。

---

## 6. 备选方案与决策

### 6.1 为什么不继续用 base64 内联？

| 维度 | base64 内联 | 设备缓存（本提案） |
|------|------------|-----------------|
| 带宽 | ×1.33 | ×1.0 |
| Gateway 内存峰值 | 全量 | 流式 |
| csc 处理路径 | 绕过 FileReadTool 优化 | 走完整优化路径 |
| 会话历史膨胀 | 严重 | 几乎无 |
| 设备磁盘占用 | 0 | 有（受配额管控） |
| 复用 | 不可 | 可 |
| 审计 | 不可 | 有 sha256 / 元数据 |

**结论**：>1MB 的图片走缓存收益显著；<256KB 的极小截图仍可保留 base64 内联作为兜底（UI 层做阈值切换）。

### 6.2 为什么不直接用 `PUT /runtime/files/content` 扩展二进制？

- 该端点语义是"编辑工作区文件"，**写入路径由客户端指定**，与"附件缓存"的隔离目标冲突。
- 扩展二进制会破坏现有调用方（UI 文件编辑器）的契约。
- 多租户/多会话无法共享同一缓存根。

**结论**：新建 `/api/v1/attachments`，保持职责分离。

### 6.3 为什么不让前端直连 gateway？

- gateway 现仅有 InternalSecret 鉴权，前端没有 InternalSecret；
- 走 server 才能叠加用户级权限（哪个用户能访问哪台设备）；
- 一致审计入口。

### 6.4 为什么不在 costrict-web server 落盘，再把 URL 给设备？

- 与 cs-cloud "代码不出本地"的核心原则冲突：附件可能包含代码截图、错误堆栈、内部文档等敏感信息；
- 多了一次云端存储成本；
- 设备离线时无法访问；
- 跨地域延迟。

**结论**：原始字节只在设备上落盘，server/gateway 仅做透传。

### 6.5 为什么不用云端对象存储 + 预签名 URL？

- 同 6.4；
- 如果未来出现"跨设备共享附件"的场景（例如把附件从一个设备移到另一个），可再引入云端临时中转，但默认走"设备↔UI"直连。

### 6.6 ULID vs UUID vs 自增整数

- ULID：自带时序、可排序、字符串安全、跨语言友好 → **采用**；
- UUID v4：随机无序，单目录文件多时不利；
- 自增整数：需要全局计数器，跨重启 / 跨设备克隆易冲突。

---

## 7. 风险与开放问题

### 7.1 已识别风险

| 风险 | 影响 | 缓解 |
|------|------|------|
| 隧道 50MB 上限把大附件挡住 | 大 PDF / 视频 / 数据集无法走第一版 | v2 引入 chunked upload（`POST /attachments/{id}/chunks`） |
| gateway 全量读 body 到内存 | 单设备并发上传时内存陡增 | v1 接受（≤50MB × N 并发），v2 改流式 |
| 缓存目录被磁盘配额限制（用户主目录已满） | 上传失败 | 上传前 `syscall.Statfs` 预检；失败时返回 507 + 友好提示 |
| csc 子进程工作目录与附件路径不同盘（Windows） | 偶发权限问题 | 落盘前对目标目录 `os.Chmod`，确保当前用户可读 |
| 多用户共享同一设备 | 越权读取 | v1 假设单用户；v2 在 `index.json` 加 `owner_user_id` 字段并叠加 server 端过滤 |
| 设备卸载/重装 cs-cloud | 附件丢失 | 文档明确说明；UI 在上传成功提示中标注"仅本设备" |
| 恶意上传伪装 MIME | 类型校验绕过 | 服务端用 `mimetype` 二次嗅探；图片走解码校验 |
| 路径穿越攻击 | 任意文件读取 | 见 §5.2 |

### 7.2 开放问题

1. **会话级附件是否要在会话结束时自动 gc？** 默认建议"不自动"，由 `expires_at` 统一管控；如要做，需在 `csc` 会话结束时回调 cs-cloud。
2. **附件 ID 是否需要在 prompt 文本中显式可见？** 当前用 `@{abs_path}` 引用，是否需要 `<attachment id="01HW...">` 形式以方便 LLM 引用？建议先不做，让 LLM 自然语言引用即可。
3. **是否支持附件之间的"引用关系"（如"图 A 是图 B 的裁剪"）？** v1 不做，未来如需可加 `parent_id` 字段。
4. **缩略图生成是否在 cs-cloud 内做？** UI 回显缩略图当前依赖原文件。建议 v1 直接走 `GET /attachments/{id}/content`；如果性能不达标，v2 增加 `?size=thumb` 由 cs-cloud 即时缩放。
5. **是否要把附件元数据上报云端？** 当前 heartbeat 不含。如需云端审计，可加一个 `attachments_summary` 字段（count/total_size，不含文件名）。

---

## 8. 实施计划（分阶段）

### Phase 1：MVP（打通图片链路）

**目标**：单张图片能从 UI 上传到设备缓存，再被 csc 读取识别。

- [ ] cs-cloud：`internal/attachments/` 新模块，提供 `Manager`（store/gc/quota）；
- [ ] cs-cloud：localserver 注册 `POST/GET/DELETE /api/v1/attachments[/:id[/content]]`，含 Swagger；
- [ ] cs-cloud：CLI 增加 `cs-cloud attachments list/prune`；
- [ ] cs-cloud：单测覆盖 happy path、路径穿越、超大文件、配额触发；
- [ ] costrict-web：server 增加 `POST /api/devices/{id}/attachments` 透传；
- [ ] costrict-web：单测覆盖鉴权失败、尺寸超限、workspace 校验；
- [ ] app-ai-native：新增 `attachment-client.ts`，`attachments.ts` 改造上传流程，失败回退 base64；
- [ ] e2e：UI 上传图片 → 看到回显 → 发送 prompt → csc 返回识别结果。

### Phase 2：通用化与运维

- [ ] 类型白名单配置化；
- [ ] 全局配额与 gc 任务上线；
- [ ] `cs-cloud status` 输出附件统计；
- [ ] 增加审计日志（上传者 user_id、session_id）；
- [ ] UI 增加附件管理面板（按设备/会话查看）。

### Phase 3：大附件与多设备

- [ ] chunked upload（>50MB 分片）；
- [ ] gateway 流式转发（替代 `io.ReadAll`）；
- [ ] 缩略图即时生成（`?size=thumb`）；
- [ ] 跨设备附件中转（云端临时存储 + 预签名 URL，仅用户显式触发）。

### Phase 4：安全加固

- [ ] 附件级 ACL（按 user_id / workspace 隔离）；
- [ ] 上传内容病毒扫描（clamav / 第三方 API）；
- [ ] 落盘加密（可选 LUKS / age）。

---

## 9. 兼容性与回滚

- **UI 兼容**：v1 UI 检测到 `404/501` 自动回退 base64 内联，老设备无感知。
- **协议兼容**：`prompt.parts[].url` 字段保持兼容；新增 `path`、`attachmentId`、`source` 字段，老版本 csc 直接忽略。
- **存储兼容**：缓存目录独立于现有 `~/.costrict/cs-cloud/{logs,data}`，删除整个 `attachments/` 目录即可回滚，不影响其他模块。
- **配置兼容**：新增配置项全部带默认值，零配置启动等同 v1 行为。

---

## 10. 关键文件清单（实施时锚点）

| 仓库 | 路径 | 改动类型 |
|------|------|---------|
| cs-cloud | `internal/attachments/manager.go` | 新增 |
| cs-cloud | `internal/attachments/store.go` | 新增 |
| cs-cloud | `internal/attachments/gc.go` | 新增 |
| cs-cloud | `internal/attachments/quota.go` | 新增 |
| cs-cloud | `internal/localserver/handlers/attachments.go` | 新增 |
| cs-cloud | `internal/localserver/router.go` | 修改（注册路由） |
| cs-cloud | `internal/platform/paths.go` | 修改（加 `AttachmentsDir()`） |
| cs-cloud | `internal/app/app.go` | 修改（装配 Manager） |
| cs-cloud | `internal/cli/attachments.go` | 新增（list/prune 子命令） |
| cs-cloud | `internal/config/config.go` | 修改（加 attachments 配置块） |
| costrict-web | `server/internal/handlers/device_attachment.go` | 新增 |
| costrict-web | `server/internal/gateway/client.go` | 修改（如需流式） |
| app-ai-native | `src/client/attachment-client.ts` | 新增 |
| app-ai-native | `src/components/prompt-input/attachments.ts` | 修改 |
| app-ai-native | `src/components/prompt-input/build-request-parts.ts` | 修改 |

---

## 11. 结论

该方案在现有架构上**新增少量模块**即可落地，关键路径完全复用现有能力：

- 隧道反向代理已支持 50MB 二进制流式；
- localserver 已有路由注册 / Swagger / 工作区沙箱模式；
- csc 已具备完整的图片读取与多模态链路；
- UI 已有完整的图片采集能力，只需把"base64 内联"换成"上传 + 路径引用"。

**核心收益**：

1. 让附件传输从"内存里走一遍"升级为"设备上有实物"，可审计、可复用、可管控；
2. 与 cs-cloud "数据不出本地"的核心合规定位一致；
3. 第一版工作量集中在 cs-cloud 一个新模块 + 一条 server 路由 + 一处 UI 改造，影响面可控；
4. 为后续支持 PDF / 录音 / 数据集等任意附件留足扩展空间。

**建议**：按 Phase 1 → Phase 2 推进，第一版只做"图片单文件上传 + 路径引用 + 基本 gc"，待链路稳定后再做大附件与运维面板。
