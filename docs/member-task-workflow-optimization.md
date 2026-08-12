# 成员任务工作流优化方案

## 1. 背景

当前成员任务 CLI 暴露了以下主要动作：

```text
list -> get -> prepare -> start -> pause -> submit/review -> delete
```

源码中这些动作并不处于同一抽象层级：

- `prepare` 会读取云端上下文、拉取任务材料、创建本地目录、生成 manifest，并以 staging + rename 的方式原子发布工作区。
- `start` 只把本地 `Activity` 从 `prepared` 或 `paused` 改为 `active`。
- `pause` 只把本地 `Activity` 从 `active` 改为 `paused`。
- `submit` 和 `review` 会生成远端操作预览，经用户确认后执行正式写操作。

`start` 和 `pause` 不会启动或暂停 Agent、进程、会话或云端任务，也不会改变实际工作区能力。与此同时，`active` 和 `paused` 最终都投影为 `in_progress`，两者没有清晰的用户可见差异。

这使用户被迫理解没有独立业务价值的中间动作，也增加了 Agent 的命令选择、状态解释和错误恢复复杂度。

## 2. 优化目标

将成员任务工作流收敛到用户意图，而不是暴露内部准备步骤：

1. 用户只表达“查看任务”“处理任务”“提交成果”“审核成果”等业务意图。
2. 用户确认处理任务后，由系统在后台准备本地工作区。
3. `prepare` 保留为内部原子能力，不再作为面向用户的独立动作。
4. 删除 `start`、`pause` 及其相关状态，不做兼容处理。
5. 保留提交、审核和删除的预览确认机制。
6. 失败时清楚说明任务是否已进入可处理状态、本地文件是否存在以及下一步动作。

## 3. 设计原则

### 3.1 用户动作必须对应真实业务效果

用户执行一个动作后，应产生能够观察和解释的结果。“开始”如果只修改本地枚举而不启动任何执行过程，就不应作为独立动作存在。

### 3.2 技术步骤由系统编排

下载材料、创建目录、生成 manifest 和发布工作区属于系统准备过程。用户确认处理任务后，系统应自动完成这些步骤。

### 3.3 只读操作不产生本地副作用

`list` 和 `get` 只读取任务信息。查看详情不能自动创建目录、下载仓库或修改本地状态。

### 3.4 高风险与远端写操作继续显式确认

任务提交、审核结论和本地成果删除仍采用“生成预览 -> 用户确认 -> 执行”的流程。后台准备不等于授权提交或覆盖本地修改。

## 4. 目标用户流程

### 4.1 处理任务

```text
查看任务列表或详情
        |
用户表达“处理这个任务”
        |
系统确认任务身份和当前可处理性
        |
后台准备工作区
  - 获取最新任务上下文
  - 下载材料或仓库
  - 创建 staging 目录
  - 生成 metadata 和 manifest
  - 归档可安全替换的旧版本
  - 原子发布正式目录
        |
返回实际工作目录和任务要求
        |
用户开始编辑或审查
```

以下表达均视为用户已经确认处理，无需再次询问：

- “处理任务 2”
- “开始做这个任务”
- “接下这个任务”
- “继续这个返工任务”
- “开始审核第 3 个任务”

以下表达仅执行只读查询，不触发后台准备：

- “查看任务 2”
- “任务 2 是做什么的”
- “看看验收标准”
- “刷新任务列表”

### 4.2 提交成果

```text
用户请求提交
    |
校验工作区、manifest、任务版本和交付物
    |
生成提交预览
    |
向用户展示成果、缺失项、影响和确认后状态
    |
用户明确确认
    |
执行正式提交并报告结果
```

提交不要求存在“已开始”状态，只要求任务已经成功准备、工作区有效且用户仍有写权限。

### 4.3 审核成果

审核任务与处理任务使用相同的后台准备入口。准备过程下载前置成果和审查材料；最终通过或退回仍必须先生成审核预览，再等待确认。

## 5. 目标状态模型

删除 `ActivityPrepared`、`ActivityActive`、`ActivityPaused` 以及 `TaskRecord.Activity`。

面向用户保留以下状态：

| 状态 | 含义 | 可用动作 |
|---|---|---|
| `not_prepared` | 尚无可用本地工作区 | 查看、处理 |
| `ready` | 工作区已准备，可直接编辑、提交或审核 | 查看、提交/审核、删除 |
| `reprepare_required` | 云端上下文变化，需要重新准备 | 查看、处理、删除 |
| `confirmation_waiting` | 已生成提交或审核预览，等待确认 | 查看、确认、重新预览、删除 |
| `submitting` | 已确认，正在执行远端操作 | 查看、恢复 |
| `sync_pending` | 操作已执行但同步结果未确认 | 查看、恢复 |
| `read_only` | 已失去处理资格，本地文件仍保留 | 查看、删除 |
| `ended` | 任务已经结束 | 查看、删除 |

状态优先级建议：

```text
ended
> submitting / sync_pending
> read_only
> reprepare_required
> confirmation_waiting
> ready
> not_prepared
```

本次采用同步 `handle`，请求返回前完成准备或返回错误，因此不增加不可查询的瞬时 `preparing` 状态。若后续改为异步 operation，再由持久化 operation/journal 推导 `preparing` 和统一的恢复状态，不能仅依赖内存状态。

## 6. CLI 设计

### 6.1 对外命令

目标命令集：

```text
cs-cloud task list
cs-cloud task get <task_key>
cs-cloud task handle <task_key> [--workdir <path>]
cs-cloud task submit <task_key> [--confirm <preview_id>]
cs-cloud task review <task_key> --decision <approve|reject> [--reason <text>]
cs-cloud task review <task_key> --confirm <preview_id>
cs-cloud task delete <task_key> [--force-discard] [--confirm <preview_id>]
cs-cloud task recover <task_key>
cs-cloud task help
```

`handle` 表达“确认处理任务”的业务意图，内部调用现有准备能力。对于评审角色，它表示“开始处理审核任务”，而不是执行审核结论。

### 6.2 删除的命令

直接删除以下命令，不保留别名、兼容转发或弃用提示：

```text
cs-cloud task prepare
cs-cloud task start
cs-cloud task pause
```

`prepare` 对应的服务实现继续保留，但只作为 `handle` 的内部能力，不进入公开命令 catalog。

### 6.3 命令结果

`handle` 成功结果至少包含：

```json
{
  "task_key": "...",
  "directory": "...",
  "state": "ready",
  "prepared": true,
  "reused": false
}
```

语义要求：

- `performed=true`：本次创建、更新或恢复了工作区。
- `outcome=already_completed`：相同版本工作区已经可用，本次直接复用。
- `outcome=recovered`：通过 journal 恢复完成。
- `state_after=ready`：用户可以开始编辑、提交或审核。

## 7. 后台准备机制

### 7.1 执行方式

“后台动作”表示由系统代替用户编排准备步骤，不代表本次引入异步队列。本次保持请求内同步执行；调用方可以展示等待态，但不能把瞬时进度持久化为任务状态。

如果准备时间经常接近当前 300 秒上限，再将 `handle` 改为异步 operation：

```text
handle -> accepted(operation_id)
get    -> preparing + progress
get    -> ready / recovery_required
```

不建议在没有进度查询、幂等 operation ID 和崩溃恢复能力之前，仅通过启动 goroutine 实现后台执行。

### 7.2 幂等性

相同 `task_key + remote_version + directory` 的重复 `handle` 应直接返回已有工作区，不重复下载或覆盖文件。

### 7.3 重新准备

- attempt 增加但上下文兼容时，保留上一轮成果并更新任务元数据。
- context version 变化且目录干净时，归档旧目录后重新准备。
- 存在未提交本地修改时停止，不自动覆盖、归档或删除。
- 存在未完成提交/审核 operation 时先恢复操作，不进入重新准备。

### 7.4 失败与恢复

准备失败必须返回：

1. 失败发生在哪个阶段。
2. 正式任务目录是否已经发布。
3. 旧目录和本地修改是否仍然保留。
4. 是否可以安全重试，或必须执行 `recover`。

staging、archive 和 journal 的现有原子性与恢复机制继续保留。

## 8. 交互与 Skill 调整

Agent 面向用户只推荐以下动作：

- 查看详情
- 处理任务
- 提交成果
- 审核成果
- 删除本地任务
- 恢复操作

Skill 中需要删除：

- “准备后再开始”的两步引导。
- “暂停后回到已准备”的描述。
- 根据 `start`、`pause` 推导状态和下一步的规则。
- `prepare`、`start`、`pause` 的自然语言快捷操作映射。

Skill 中需要新增：

- “处理/接下/开始做/继续返工/开始审核”统一映射为 `handle`。
- `handle` 是用户确认后的系统准备动作。
- 查看列表和详情永远不自动触发 `handle`。
- `handle` 成功后展示实际目录、任务目标、验收条件和下一步，不再提示执行“开始”。
- 重新准备遇到本地修改时停止并说明风险，不替用户做覆盖决策。

## 9. 源码改造范围

### 9.1 `internal/membertask`

- 删除 `Activity` 类型及三个枚举值。
- 删除 `TaskRecord.Activity`。
- 删除 `Service.Start` 和 `Service.Pause`。
- 将 `StatusPrepared` 重命名为 `StatusReady`。
- 调整 `ProjectStatus`，只根据真实事实投影状态。
- 调整 `availableActions`：`not_prepared` 和 `reprepare_required` 提供 `handle`；`ready` 直接提供 `submit` 或 `review`。
- 将 `PrepareWithFacts` 重命名为面向应用层的 `Handle`，内部继续复用 `prepare` 原子实现。

### 9.2 `internal/cli`

- 从 help catalog 删除 `prepare`、`start`、`pause`。
- 增加 `handle` 命令及 `--workdir` 参数。
- 删除相关参数解析、响应解码和重试分支。
- 更新文本状态映射：`prepared` 改为 `ready`。
- 更新 next commands，禁止再生成 `start`、`pause` 或公开 `prepare`。

### 9.3 `internal/localserver`

- 删除 `/start` 和 `/pause` action。
- 将公开 `/prepare` action 替换为 `/handle`。
- `/handle` 调用成员任务服务的 `Handle`。
- 保留 prepare journal 的恢复入口，但不再暴露为用户动作。

### 9.4 数据结构与本地存储

本次不做兼容处理，因此直接升级本地任务 store schema：

- 保持 CLI/云端协议 `SchemaVersion=1.0`，单独将本地 `StoreSchemaVersion` 提升为 `2.0`。
- 新结构不再读取 `activity` 字段。
- 启动时发现旧 schema，明确报告本地任务缓存版本不支持。
- 用户重新执行 `handle` 创建新结构的工作区。

如果旧工作区包含用户成果，不应自动删除。可将旧 store 标记为不可写并提示其实际目录，由用户自行迁移或清理。

## 10. 删除与保留清单

| 项目 | 处理方式 | 原因 |
|---|---|---|
| prepare 核心实现 | 保留 | 有真实的下载、校验、目录发布和恢复职责 |
| prepare 公开命令 | 删除 | 技术动作不应暴露给用户 |
| handle 公开命令 | 新增 | 表达用户确认处理任务的业务意图 |
| start 命令与服务 | 删除 | 只修改本地枚举，无实际启动行为 |
| pause 命令与服务 | 删除 | 不暂停进程、Agent 或云端任务 |
| Activity 字段 | 删除 | 不再参与有效状态推导 |
| submit/review 预览确认 | 保留 | 属于远端写操作和业务决策 |
| delete 预览确认 | 保留 | 可能丢失本地成果 |
| prepare journal | 保留 | 支撑原子发布和崩溃恢复 |
| recover | 对外保留 | 处理结果未知或中断操作 |

## 11. 实施顺序

1. 修改领域模型，删除 `Activity`，重写状态投影和动作矩阵。
2. 将应用层 `PrepareWithFacts` 调整为 `Handle`，保留内部 `prepare` 实现。
3. 修改 localserver action，只保留 `/handle`。
4. 修改 CLI catalog、参数解析、响应解码、输出和 next commands。
5. 更新 cs-cloud-task Skill 的触发词、状态解释和动作映射。
6. 删除 `start/pause` 相关测试，新增 `handle` 和精简状态机测试。
7. 提升 store schema 主版本并验证旧版本拒绝行为。
8. 运行成员任务相关测试，不执行全量测试。

## 12. 测试方案

### 12.1 领域测试

- 未准备任务执行 `Handle` 后进入 `ready`。
- 相同版本重复 `Handle` 返回 `already_completed`。
- 准备中断后能够通过 journal 恢复。
- 上下文变化且目录干净时安全重新准备。
- 上下文变化且存在本地修改时拒绝覆盖。
- `ready` 状态下 worker 可直接提交，critic 可直接审核。
- 状态投影和动作列表中不再出现 `start`、`pause`、公开 `prepare`。

### 12.2 CLI 测试

- catalog 只包含目标命令集。
- `handle <task_key> [--workdir]` 参数正确解析。
- `prepare`、`start`、`pause` 返回 unknown command。
- `handle` 的 envelope 正确反映 `performed`、`outcome`、目录和 `state_after`。
- next commands 不生成已删除命令。

### 12.3 交互测试

- “查看任务 2”只调用 `get`。
- “处理任务 2”调用 `handle`。
- “开始做任务 2”调用 `handle`，不调用已删除的 `start`。
- “继续返工任务 2”调用 `handle` 并保留已有成果。
- `handle` 成功后直接展示目录和工作要求。
- 本地修改冲突时停止并提示用户，不自动覆盖。

## 13. 验收标准

- 用户完成任务不再需要理解或执行“准备、开始、暂停”三个中间动作。
- `prepare` 仍作为可靠的内部原子能力完成物料落地和恢复。
- 公开 CLI 不包含 `prepare`、`start`、`pause`。
- 用户确认“处理任务”后，系统自动完成准备并返回可工作的实际目录。
- 查看任务不会产生本地写入。
- 提交、审核、删除仍保留预览确认和过期校验。
- 源码中不再存在仅用于 `start/pause` 的 `Activity` 状态。
- 不提供旧命令、旧状态或旧 store schema 的兼容路径。

## 14. 最终建议

采用“业务动作对外、技术动作对内”的模型：

```text
用户：查看 -> 处理 -> 提交/审核 -> 结束
系统：查询 -> 后台 prepare -> 校验/预览 -> 确认执行 -> 恢复/清理
```

`prepare` 应保留，但转为用户确认处理任务后的系统动作；`start` 和 `pause` 应从命令、状态、API、测试和 Skill 中完整删除。这样可以减少无效状态转换，同时保留现有工作区准备、原子发布和异常恢复能力。
