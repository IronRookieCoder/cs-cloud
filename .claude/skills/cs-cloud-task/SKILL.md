---
name: cs-cloud-task
description: Use when a user mentions cs-cloud or Multica tasks, assigned work, task numbers, or cs-cloud task CLI output/errors and asks to list, inspect, prepare, submit, review, recover, or delete a task; do not use for generic TODO or programming task management.
---

# cs-cloud 本地协作任务

按以下用户旅程推进任务：**查看任务列表 → 查看任务详情 → 在本地处理任务 → 提交或审查**。
每轮只推进一个阶段；不要把用户一句话中的“处理并提交”当作提交确认。

底层命令、argv、信封、确认、恢复和文件交付规则必须遵守 [CLI 契约](references/cli-contract.md)。

## 快速执行

- `list` 直接执行 `cs-cloud task list --json`；不要先调用根命令 `--help`、`task --help` 或 `task help`。
- 已有可信序号绑定时，`get` 直接使用绑定的完整 `task_key`；不要重新 `list`。
- 直接从 `PATH` 调用 `cs-cloud`；只有出现 command-not-found 时才做一次程序定位，不要搜索用户目录。
- 只有遇到未知 schema、未知参数/动作或契约明确要求重新获取 catalog 时，才调用一次 `cs-cloud task help --json`；不得尝试多种 help 形式。
- 常规 `list` 只允许一次 CLI 调用和一次解析路径：先解析 stdout 顶层信封，再从 `data.result` 读取数组（只有 `data.result` 明确不是数组时才按契约失败停止）。工具持久化大输出时，只对已生成文件做一次定向读取；不要尝试 `jq`、`node`、PowerShell 等多个解析器，也不要重复读取或改猜 JSON 路径。过滤、计数、排序和序号绑定必须由结构化数据计算，不得人工扫描、手工重建数组或估算。
- 不向用户播报“读取契约”“定位程序”“获取 catalog”“解析输出”等例行步骤；成功时直接给结果。

## 1. 查看任务列表

把 CLI 作为事实来源。默认只展示云端仍分配、且存在用户可执行动作的任务，过滤：

- 已完成、云端已删除、已不再分配的任务；
- 仅本地保留的历史记录；只有用户明确要求历史/本地任务时才展示。

完成过滤、排序、去重后重新生成用户序号，并保存“序号 → 完整 `task_key`”绑定。CLI 原始数组下标、标题、workspace、截断 UUID 都不是序号。刷新、过滤或绑定失效后作废旧序号，要求用户重新选择，不得把旧序号套到新列表。

列表使用“摘要行 + 必要时补充行”，公共说明只写一次。每项必须优先使用真实业务字段，按以下顺序拼接标题：`issue_identifier + issue_title` → `remote.title` → `remote.node_name` → `未命名任务`；标题相同才追加 `task_key` 末 8 位。该后缀是唯一允许展示的内部标识例外，只能用于重复标题消歧，不得用于普通标题、任务选择或 CLI 传参。阶段使用 `remote.task_kind`、`role` 和 `attempt` 组合：`review`/`critic` 为“成果审查”，`rework` 或 `attempt > 1` 为“返工”，其余为“首次处理”，无可靠信号为“待确认”。状态和动作只由 `projection.available_actions` 推导，并将 `flags` 转为短提示（如“离线”“有本地修改”）。

任务超过 8 项时按“成果审查、返工、首次处理、待确认”分组；组内按优先级和 CLI 返回顺序稳定排序。列表只展示摘要字段，不读取详情、不生成目标占位文本；详情中的目标、验收和交付物留到 `get` 阶段。公共说明只写一次，不要为每个任务重复相同文案：

状态和推荐动作按首个命中项确定：含 `review` 为“可审查 → 审核”，含 `submit` 为“可提交 → 提交”，含 `handle` 为“待处理 → 处理”，仅含 `get` 为“可查看 → 查看”；若同时含 `get` 和 `handle`，推荐“处理”，把“查看”作为次要动作。未知动作如实写成“可操作”，不得臆造状态。

```text
当前需处理：<N> 项（成果审查 <A>｜返工 <B>｜首次处理 <C>；历史 <M> 项已隐藏）
1. <Issue 编号 标题 - 任务标题>｜<成果审查/返工/首次处理/待确认>｜<状态> → <动作> <attempt/flags 短提示>
2. <任务标题>｜<类型>｜<状态> → <动作>｜标识:<仅重复标题时显示末 8 位>

列表不含完整要求；查看任务后展示目标、验收条件和前置成果。
```

任务类型按可靠信号判断，优先级为“成果审查 > 返工 > 首次处理”；无法判断时写“待确认”，不根据标题或状态名猜测。列表没有目标时写“查看详情了解任务要求”。

列表末尾只给用户可直接回复的选项，例如：`查看 2｜处理 2｜刷新`。动作必须来自 CLI 返回的可执行动作；同一任务有多个动作时只突出首个推荐动作，并把其余动作放入括号。不要在列表阶段逐项生成目标占位文本。

## 2. 查看任务详情

用户选择序号后，先获取并展示详情；查看本身不得触发 `handle`、`submit` 或 `review`。动作前后都确认使用同一个完整 `task_key`，并核对返回的 `data.task_key` 完全匹配。

按固定顺序输出：

```text
任务：<标题>
这是：<首次处理｜返工｜成果审查｜待确认>；你的角色：<执行者｜审查者｜待确认>

要完成的结果：<objective/goal 的中文摘要>
完成标准：<acceptance_criteria>
需要交付：<required_deliverables>（标出缺失项）
前置成果/参考输入：<predecessor_results>
返工原因：<rework_reason，仅返工时展示>
本地位置：<local.directory，已准备时展示>

现在：<当前状态和 flags>
建议：<下一动作>
这一步会：<动作效果>
之后：<下一阶段>
```

英文目标、验收条件和交付物要给中文摘要，必要时保留原文依据，不直接倾倒 JSON。缺少目标、验收条件，或任务声明为必需的交付物/前置成果实际不可用时，明确标记“任务说明不完整”，暂停准备并建议查看详情或联系任务发起人。任务未声明前置成果时不得误判为缺失。

## 3. 在本地处理任务

只有用户明确要求开始处理后才执行 `handle`。执行前检查 CLI 返回的动作和 flags：`cloud_state_unverified`、`write_authority_lost`、`dirty` 或目录冲突会阻断可能覆盖本地成果或推进云端的操作；`offline` 会阻断云端写入和需要联网的准备，但已准备目录仍可继续本地编辑。不要覆盖、回滚、清理用户修改。

把 `handle` 解释为一次性准备工作区：创建或更新本地任务目录、下载任务材料、落盘前置成果和交付物目录、写入 manifest；返工时保留上一轮成果。它不会提交或覆盖云端结果。

准备前置成果时要求同一版本的正文、SHA256 和来源（provider、repository、ref、commit、path）。正文不可用且没有明确空正文标记时停止并报告 `material_content_unavailable`。准备前后摘要不一致时报告 `input_material_modified`，不得提交旧材料或覆盖本地修改。

准备成功后固定报告：

```text
本地任务已准备：<实际目录，可在 IDE 打开>
已准备：<任务材料、前置成果及版本/来源>
交付物：<required/optional 文件状态，标出缺失项>
当前：<可编辑｜返工中｜离线可编辑等>
下一步：在本地完成任务后请求提交预览或审核预览。
```

目录内文件交付必须显式绑定，不扫描目录；具体路径、摘要、大小和 manifest 规则见 CLI 契约。

## 4. 提交或审查

根据 CLI 返回的角色和动作选择一个分支：worker 提交成果，critic 审查成果。两者都必须先预览，再单独取得确认。

### 提交成果

首次 `submit` 不带 `--confirm`，只生成业务化预览。预览必须清楚展示将提交的文件、仓库、交付物绑定、缺失/非法项、版本或摘要、影响对象和确认后的工作流状态。`files=[]` 且 `repositories=[]` 时明确标记“空交付”；普通“提交/确认”不等于确认空交付。

只有服务端 finalize 成功且返回完整 publish plan、submission 和下一节点状态后，才能报告提交完成。

### 审查成果

首次 `review` 只生成预览，展示被审查成果、验收条件对应情况、拟通过/退回结论、退回原因和对后续角色的影响。退回必须有明确原因；确认后才执行结论。

### 共同闸门

- 预览内容、目标任务、候选 argv 或快照变化，或出现 `preview_stale`/`review_snapshot_stale` 时，丢弃旧确认并重新预览。
- `previewed`、`performed=false`、超时、网络中断或响应丢失均表示结果尚未确认；保留本地成果，优先恢复原 operation，不创建重复 submission。
- 删除、强制丢弃和恢复也必须遵守契约中的预览、风险提示和幂等规则。

## 状态、失败与输出

只根据 CLI 的 `available_actions`、`outcome`、`performed` 和 flags 推导用户状态，不照抄内部枚举。`observed` 表示仅查询，`previewed` 表示尚未执行，`completed` 且 `performed=true` 才能报告完成；`recovered` 和 `already_completed` 按实际含义说明。

调用首次返回退出码 `2`、`invalid_arguments`、`invalid_task_key`、无效信封或未知协议时立即停止本次请求：不换参数形式、不追加 `--help`、不猜分隔符、不搜索内部目录、不重复调用。只有获得新的可信输入后，才作为新请求重新校验。

异常回答不超过三段中文，并回答：**发生了什么、我的本地文件是否还在、现在能做什么**。错误代码只能作为括号中的辅助信息，不能替代业务语言。

禁止访问 Daemon HTTP/SSE、配置端口、local secret 或内部状态目录；禁止使用 curl、PowerShell Web cmdlet、shell 字符串拼接、stderr 推断状态或 `2>&1`。
