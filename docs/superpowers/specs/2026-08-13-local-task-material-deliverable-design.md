# 本地任务材料解析与交付闭环设计

**日期：** 2026-08-13  
**范围：** `cs-cloud` 与 `multica-1` 的 member-task 双仓协同改造  
**依据：** `bug/local-task-deliverable-investigation.md`、`docs/workflow-deliverable-complete-analysis.md`

## 1. 目标与非目标

### 1.1 目标

解决本地任务的两个业务断点：

1. 云端前置任务材料通过 PR/MR 传递时，本地可以获得真实正文、版本和摘要，而不是落盘为空文件。
2. 本地生成的交付物可以明确绑定到云端 runtime deliverable，经过 preview、confirm、operation 和 finalize 后，写入 Workflow 正式 submission，并进入现有审核流程。

### 1.2 非目标

- 不把本地任务目录整体自动上传到云端。
- 不让 member-task 绕过自身 operation 模型直接调用 `workflow deliverable submit`。
- 不自动猜测一个本地文件对应哪个交付物。
- 不改变已有 repository-only member-task 提交流程。
- 不在本次改造中改变 Workflow 的交付物定义、审核或归档规则。

## 2. 根因与现状边界

调查实例暴露出两个独立问题：

- 云端 `GetContext` 为 predecessor result 返回了 PR URL，但 file material 的 `content` 为空；本地 `materializeSources` 仍按 `content` 写文件，所以得到标准空文件 SHA256 `e3b0c442...`。
- 本地 `buildSubmitManifest` 只收集 manifest 中 `MaterialOutputWritable` 文件和 repository 事实。任务根目录中新建的 `chinese_chess.html` 不在 manifest，故 `SubmitPreviewRequest` 的 `files` 与 `repositories` 均为空；云端可以推进无 requirement 的状态，但不会产生 submission。

Workflow 已证明正式交付至少需要两类事实：远端 Git 内容载体和后端 submission 记录。member-task 云端已有 `FinalizeMemberTaskWorker`，能够将 file manifest 和 repository PR 事实写入 Workflow submission，并执行 required deliverable 门禁。因此本方案在 member-task 外层补齐契约，不另建交付存储。

## 3. 方案决策

### 3.1 备选方案

| 方案 | 做法 | 优点 | 风险 | 结论 |
|---|---|---|---|---|
| A. 扩展 member-task 契约 | 云端解析材料；本地用 `--deliverable + --file` 绑定；复用现有 operation/finalize | 权限、摘要、幂等和恢复边界清晰；复用 Workflow 归档 | 需要双仓同步升级 | **采用** |
| B. 直接调用 workflow CLI | member-task 提交时绕过 preview/confirm，直接创建 PR 并登记 | 表面复用已有提交代码 | 形成两套状态源，破坏 member-task 版本和恢复语义 | 不采用 |
| C. 自动扫描目录上传 | 提交时收集未跟踪文件 | 用户操作少 | 无法可靠绑定 requirement，可能上传临时文件或敏感数据 | 不采用 |

### 3.2 核心原则

- **云端权威解析：** 本地不根据任意 URL 自行抓取材料；云端负责权限、PR 状态、文件路径和内容版本。
- **本地显式选择：** 只有显式的 runtime deliverable ID 与本地文件绑定才会进入提交 manifest。
- **操作一致性：** 继续使用 preview -> confirm -> operation -> finalize；所有远端写操作保持可恢复和幂等。
- **Workflow 事实不变：** 正式成功以 `workflow_node_run_deliverable_submission` 和对应远端 PR/MR 为准，不以本地文件或 commit 为准。

## 4. 架构与数据流

```text
前置节点 PR/MR
      |
      v
multica member-task GetContext
  - 校验 PR 权限和状态
  - 解析最终文件、commit、path
  - 生成材料快照与 material_digest
      |
      v
cs-cloud task handle
  - 校验 source/sha256
  - 写入 input/predecessors
  - 写入 manifest/task metadata
      |
      v
本地任务工作目录
  - Agent 生成成果
      |
      v
cs-cloud task submit --deliverable d --file path
  - 校验路径与绑定
  - 计算文件摘要
  - submit/preview
  - confirm operation
      |
      v
multica member-task finalize
  - 校验文件和 repository 事实
  - FinalizeMemberTaskWorker
  - 写入 Workflow submission
  - 检查 required deliverables
      |
      v
awaiting_critic -> approved/completed
```

## 5. 云端材料快照契约

### 5.1 Context 返回模型

`multica-1/server/internal/membertask` 的 predecessor result 和 file material 应补充来源和版本字段。推荐形态：

```json
{
  "id": "deliverable-id",
  "title": "方案设计文档",
  "url": "https://git.example/pulls/3",
  "source": {
    "provider": "gitea",
    "repository": "workspace/wf-archive",
    "ref": "refs/heads/node/abc",
    "commit": "<full-sha>",
    "path": "nodes/1/design.md"
  },
  "content": "file body",
  "sha256": "<hex sha256>",
  "version": "<full-sha>"
}
```

对现有 Go 类型的最小调整：

- `server/internal/membertask.Deliverable` 增加 `Source` 结构（provider、repository、ref、commit、path）。
- `URL` 继续保留，作为可展示的 PR/MR 地址。
- `Content`、`SHA256`、`Version` 必须描述同一个解析时刻的正文。
- `MaterialDigest` 的 canonical 输入必须包含 deliverable identity、正文摘要、source commit、source path 和 repository identity。

### 5.2 解析规则

1. 根据已登记 submission 的 PR/MR URL 和平台 provider，解析 PR 的 head/base、最终可见 commit 和变更文件。
2. 只能选出唯一的交付文件；多文件或路径无法映射到一个 predecessor requirement 时返回明确错误，不返回空正文。
3. 读取文件内容并限制单文件与总材料大小；计算 SHA256。
4. PR 已关闭、被删除、无权限读取或文件不存在时，返回 `material_content_unavailable`，并标记是否可重试。
5. 云端无法确认内容确实为空时，禁止将空字符串作为成功材料快照。
6. 对 Gitea repository 类型材料继续使用 exact SHA clone，不经过 file-content 解析分支。

### 5.3 本地落盘规则

`cs-cloud/internal/membertask.prepareSources` 将 predecessor 转换为 `MaterialSource` 时保留 source metadata、版本和摘要；`materializeSources`：

- 写入 `input/predecessors/<safe-deliverable-id>.md`；
- 写入前校验云端 `sha256` 与正文一致；
- 将相同摘要写入 `manifest.json` 的 `sha256` 与 `origin`；
- 若云端明确声明正文为空，允许写入空文件，但 manifest 必须保存 `content_verified=true` 或等价状态；
- 若旧任务已存在空文件且云端版本/摘要变化，重新 materialize 并更新 manifest；不静默覆盖用户已经修改的 reference-only 文件，修改时返回 `input_material_modified`。

## 6. 显式交付物提交契约

### 6.1 CLI

新增参数：

```bash
cs-cloud task submit <task-key> \
  --deliverable <runtime-deliverable-id> \
  --file <local-path>
```

允许重复参数表达多个绑定：

```bash
cs-cloud task submit <task-key> \
  --deliverable d1 --file chinese_chess.html \
  --deliverable d2 --file design.md
```

`--file` 必须解析为任务目录内路径；禁止绝对路径逃逸、符号链接逃逸和任务元数据文件作为交付物。未显式绑定的未跟踪文件只用于诊断，输出建议命令，不加入 manifest。

### 6.2 本地 SubmitManifest

`cs-cloud/internal/membertask.SubmitFile` 扩展为：

```go
type SubmitFile struct {
    DeliverableID string `json:"deliverable_id"`
    Version       string `json:"version"`
    Name          string `json:"name,omitempty"`
    RelativePath  string `json:"relative_path"`
    SHA256        string `json:"sha256"`
    Content       string `json:"content"`
}
```

`Version` 推荐使用 `sha256:<digest>`；`Name` 用于云端归档文件名，`RelativePath` 仅用于本地审计和输入来源，不作为云端路径直接写入。

### 6.3 云端校验

`multica-1/server/internal/membertask.validateSubmitManifest` 必须：

- 校验 deliverable ID 属于当前 node run 的 required 或 optional requirement；
- 同一 deliverable 只允许一个 file，除非未来 requirement 明确支持多文件；
- 校验 `Version` 非空、SHA256 是 32 字节十六进制摘要，且与 `Content` 一致；
- 应用单文件 1 MiB、总文件 4 MiB 的现有限制；
- 校验文件名是安全 basename，拒绝路径穿越；
- required deliverable 若处于 missing，必须在 manifest 中出现；
- 对 repository-only 任务保持原有 repository manifest 校验。

### 6.4 Finalize

`FinalizeMemberTaskWorker` 保持为唯一的 worker finalize 入口：

1. 再次校验 accepted publish plan 中的文件摘要；
2. 将 file submission 转换为 `service.MemberTaskSubmission{DeliverableID, Content}`；
3. 与 repository PR 事实在同一事务中调用 Workflow service；
4. Workflow service 写入 `multica_workflow_node_deliverable_submission`，并检查 required requirement；
5. 成功后推进 `awaiting_critic`，失败则保留 operation 为可恢复状态。

## 7. 状态、错误与恢复

| 错误码 | 触发条件 | 本地动作 |
|---|---|---|
| `material_content_unavailable` | 云端无法解析前置文件 | handle 失败，不生成伪材料 |
| `input_material_modified` | reference-only 文件或云端摘要变化 | 阻止提交，要求重新 handle/review |
| `deliverable_binding_invalid` | ID 不属于当前 context、重复绑定或路径非法 | preview 失败并输出具体绑定 |
| `file_too_large` / `manifest_too_large` | 超过大小上限 | preview 失败，不创建 operation |
| `operation_result_unknown` | confirm 响应丢失 | 保留 operation，使用 recover |
| `cloud_unavailable` | 网络或服务暂不可用 | 保留本地状态，允许重试 |
| `required_deliverable_missing` | finalize 门禁未满足 | operation 不完成，补交后重新 preview |

幂等键沿用 `preview_id` / `operation_id`。重复 confirm 不创建重复 submission；相同文件摘要和同一 deliverable 的重试应返回已完成或可恢复状态。

## 8. 兼容与迁移

- 保持旧 manifest schema 的读取能力；新写入 schema 版本提升为 `2.1`。
- 旧客户端未传 `files` 时，repository-only 流程继续可用。
- 旧任务目录中存在空 predecessor 文件时，仅在云端 source version/digest 变化且本地 reference-only 文件未被修改时自动刷新；已修改则返回 `input_material_modified`。
- 空 publish plan 不再被当作有文件交付的成功提交；如果 context 有 required file deliverable，服务端必须拒绝 finalize。
- 不修改 Workflow 定义快照、审核、合并和归档语义。

## 9. 测试与验收

### 9.1 `cs-cloud`

- `internal/membertask/prepare_test.go`：PR 文件正文、摘要校验、明确空正文、版本刷新、修改保护。
- `internal/membertask/operation_test.go`：显式绑定文件、重复 deliverable、未跟踪文件只诊断、repository-only 兼容。
- `internal/cli/member_task_test.go`：重复 `--deliverable/--file` 参数解析、路径越界和确认命令。
- `internal/membertask/cloud_test.go`：新 context 字段的序列化与版本校验。

### 9.2 `multica-1`

- `server/internal/membertask/service_test.go`：predecessor PR 解析、source/commit/path/content/sha256 一致性。
- `server/internal/membertask/operation_test.go`：文件 manifest 校验、重复 ID、大小限制、required 门禁。
- `server/internal/handler/member_task_test.go`：context、preview、confirm 的 HTTP 契约。
- `server/internal/service/workflow_deliverable_repo_test.go`：FinalizeMemberTaskWorker 写入 submission 并推进状态。

### 9.3 跨仓集成验收

使用一个固定测试 fixture 验证：

```text
GetContext
  -> cs-cloud handle
  -> 本地生成交付文件
  -> submit preview
  -> confirm
  -> FinalizeMemberTaskWorker
  -> required submission 存在
  -> node run = awaiting_critic
```

只运行上述相关模块测试，不做全量测试。验收必须同时检查：本地 `manifest.json` 摘要、云端 operation publish plan、Workflow requirement/submission 和最终 node run 状态。

## 10. 分阶段实施顺序

1. 云端先实现材料解析和 source snapshot，增加 context fixture 与接口测试。
2. 本地增加新字段、摘要校验和旧任务刷新逻辑。
3. 本地 CLI 增加显式多文件绑定参数及任务目录路径校验。
4. 云端扩展 SubmitManifest 校验与错误码，本地生成新 manifest。
5. 复用并补强 FinalizeMemberTaskWorker 的文件 submission 与 required 门禁测试。
6. 增加跨仓集成测试和迁移说明。
7. 灰度启用：先对新任务启用强校验，再观察旧任务恢复和空提交告警，最后收紧空 publish plan 行为。

## 11. 风险与监控

- PR 多文件无法唯一映射：记录 node run、deliverable、PR URL 和候选路径，禁止静默选择。
- 文件内容过大：返回可操作错误并记录大小，不截断。
- confirm 后云端不可用：依赖 operation 状态恢复，不重复创建 submission。
- 本地文件含敏感信息：显式绑定降低误上传面；云端仍需按现有审计和大小策略记录。
- 旧客户端大量空提交：统计 `deliverable_binding_invalid` 与空 manifest 次数，灰度期间保留诊断信息。

## 12. 验收标准

本方案完成后必须满足：

1. 调查实例中的 predecessor 文件不再以空内容落盘，且本地 SHA256 等于云端正文 SHA256。
2. `chinese_chess.html` 通过显式 `--deliverable + --file` 可以进入 publish plan。
3. confirm 完成后云端存在对应 Workflow submission，交付物数量不再为 0。
4. required deliverable 未提交时，任务不能进入 `awaiting_critic`。
5. 断网、重复 confirm、进程重启后，operation 可通过 recover 收敛到正确终态。
6. repository-only 任务与旧 manifest 任务保持兼容。
