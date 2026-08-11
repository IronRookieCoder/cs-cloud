---
name: cs-cloud-task
description: Use when a user needs to view, prepare, start, pause, submit, review, or delete a Multica task assigned to them through the local cs-cloud CLI, including first work, rework, critic review, recovery, and force-discard requests.
---

# cs-cloud 本地协作任务

## 核心原则

直接调用 `cs-cloud task` 的公共 CLI API。把 Multica 和 CLI 返回的状态视为权威事实；不要复制状态机、调用 Daemon HTTP、读取 local secret，或用模型推断操作结果。

## 依赖边界

仅依赖以下 `cs-cloud` 公共 CLI 契约：

- `cs-cloud task help --json` 返回顶层 `{schema_version, commands}`；本 skill 支持 schema 主版本 `1`。每个命令仅依赖 `name`、`summary`、`capability`、`timeout_seconds`、`arguments`、`mutually_exclusive`、`confirmation_mode` 和 `outcomes`。
- catalog 声明的任务命令及其 `--json` 统一信封。
- 任务信封顶层固定为 `schema_version`、`ok`、`data` 和 `error`。`data` 包含 `request_id`、argv 数组 `command`、`task_key`、`performed`、`outcome`、`state_before`、`state_after`、`observed_at`、`result` 和 `next_commands`。

命令集合、具体参数和业务状态不属于 skill 自身知识，始终在运行时从 catalog 获取。只按 catalog 的参数元数据构造 argv；不要从帮助文案、内部实现或失败信息猜测语法。不要依赖配置文件布局、端口、HTTP/SSE 路由、Daemon 传输、Go 类型、数据库或本地状态目录；这些内部实现变化不应要求修改 skill。

按以下公共 schema 解释每个参数：

- `name` 是供条件和互斥规则引用的逻辑名；`kind` 是 `positional` 或 `flag`。
- `flag` 是 flag 参数的完整 argv token；不得从 `name` 推导拼写。
- `type` 是 `string`、`enum` 或 `boolean`；`enum` 的值只能从 `enum` 列表选择。
- 字符串和枚举 flag 的 `value_style=equals_or_unprefixed_separate` 表示 CLI 接受 `--flag=value`，也接受值不以 `--` 开头时的 `--flag value`；构造新 argv 时固定使用前者，解析 CLI 返回 argv 时按此约束接受两者。
- `required`、`required_when` 和 `required_unless` 决定何时必须取得值；`required_when` 通过 `{argument, equals}` 引用另一参数。
- 命令级 `mutually_exclusive` 中每一组逻辑名不得同时出现。

按 `arguments` 顺序生成 argv：位置参数追加值；`equals_or_unprefixed_separate` 的字符串和枚举 flag 追加单个 `flag=value` token；值为 true 的布尔 flag 只追加 `flag`。必填值缺失时向用户询问，不启动目标命令。遇到未知 `kind`、`type`、`value_style`、条件形式或 catalog 主版本时停止并报告客户端契约不兼容，不要猜测或回退到内部接口。

## 执行入口

从受信任 `PATH` 或入口配置解析一次 `cs-cloud`，规范化为绝对真实路径；同一次用户操作（包括预览和后续确认）始终复用该路径，不自动下载、升级或切换二进制。

使用支持 argv 数组且不启用 shell 的进程工具执行完整结构化 catalog：

```text
<resolved-cs-cloud> task help --json
```

从 catalog 选择目标命令后，直接调用 CLI API：

```text
<resolved-cs-cloud> task <command> [arguments...] --json
```

每次调用都传且只传一个 `--json`，并按 catalog 的 `timeout_seconds` 设置有界超时和短暂退出宽限。超时后终止并等待原进程退出；不要让它在后台继续。分别保留 stdout、stderr 和退出码，仅将 stdout 解析为 JSON。只使用 catalog 参数元数据、用户明确提供的值和 CLI 返回的结构化 argv；CLI 以退出码 `2` 拒绝的参数不得换一种猜测形式重试。不要解析或执行 stderr 的 `next` 文本。读取 catalog 后，若用户已明确要求非终态操作，必须继续执行一次目标命令，不能停在计划或帮助信息。

## 操作流程

1. 读取 catalog，并根据 `name`、`summary`、`capability` 和 `confirmation_mode` 将用户意图映射到其中声明的命令。仅查看详情时选择观察类 capability，不要触发准备或写操作。
2. 按目标命令的 `arguments` 和 `mutually_exclusive` 校验用户输入并构造 argv；不要在 skill 中维护具体参数表或额外约束。
3. 以 argv 数组和已解析绝对路径启动目标命令，使用 `timeout_seconds`，并分别保留 JSON stdout、面向人的 stderr 与退出码。
4. 按 catalog 声明的 outcomes 和下面的事实语义汇报，不把“准备调用”、无输出、预览或模拟结果说成已执行。
5. 操作后仅在返回的结构化动作或安全建议明确要求刷新时，从 catalog 选择对应的只读命令。

## 终态确认

catalog 标记为终态确认、外部可见写入、删除或强制丢弃的操作始终使用两阶段流程：

1. 首次调用不传 `--confirm`，生成最新预览。
2. 完整展示信封 `data.result` 中的结构化预览事实，并用 `data.next_commands` 说明待确认动作；不要假设 `result` 的内部字段布局。明确说明尚未完成。
3. 在预览展示之后，单独询问用户是否确认。原始请求中的“直接提交”“不用再问”不代替这次确认。
4. 终态预览的 `data.next_commands` 必须恰有一个 `safety=requires_confirmation` 的候选。仅在当前会话中保存刚展示的结构化预览 `data.result`、该候选 argv 和本次操作解析出的二进制绝对路径，不写入文件，也不向用户展示路径；候选缺失或不唯一时按 CLI 契约无效停止。
5. 用户确认后，继续使用保存的绝对路径。校验候选 argv 是非空字符串 token 数组，前三项是逻辑 `cs-cloud task`，命令存在于 catalog 且等于预览信封 `data.command` 的第三项；要求 `--json` 最多出现一次，匹配参数时忽略该输出标志，再按命令参数元数据解析 argv，并要求其中的 task key 等于预览信封 `data.task_key`。任一 token 为空或校验失败时停止。用绝对路径替换首个逻辑名后以 argv 数组直接执行；若 argv 不含 `--json`，仅在末尾追加一次，若已含一个则保持不变。除首个可执行文件路径与 JSON 输出标志外，不要重新构造、删改或覆盖决定、reason、snapshot、删除模式及其他参数。
6. 结构化预览 `data.result`、待确认 argv 或目标命令发生变化，或 CLI 返回 `preview_stale` / `review_snapshot_stale` 时，丢弃旧确认并重新预览。

## 结果事实

| 条件 | 可报告事实 |
|---|---|
| 退出码 `0`、`ok=true`、`data.outcome=observed`、`data.performed=false` | 已完成查询；没有推进业务状态 |
| `data.outcome=previewed`、`data.performed=false` | 预览已生成，正在等待用户确认 |
| `data.outcome=completed` 且 `data.performed=true` | 本次调用已完成并推进对应操作 |
| `data.outcome=already_completed` | 操作此前已经完成；不要声称本次重新执行 |
| `data.outcome=recovered` 且 `data.performed=true` | 已恢复并收敛原 operation；按返回状态说明结果 |
| 任意非零退出、`ok=false` 或信封无效 | 操作未完成；展示结构化 `error` 和安全下一步，不要声称已执行 |

`data.performed=false` 永远不能证明本次推进了状态。`data.command` 必须是以 `cs-cloud task <command-name>` 开头的非空 argv 数组；`next_commands` 位于 `data`，不在 `error` 内。只有在 stdout 非空、JSON 可解析、schema 主版本兼容、必需字段和枚举有效，且退出码与 `ok` 一致时，才按信封报告结果；否则视为客户端契约不兼容或调用失败。

## 建议命令

程序判断只使用顶层 JSON `error.code` 和 `data.next_commands`：

| `safety` | 行为 |
|---|---|
| `observe_only` | 最多自动执行一个；可观察或恢复已受理的原 operation |
| `preview_only` | 最多自动执行一个；展示新预览后等待独立确认 |
| `idempotent_recovery` | 先向用户说明，再执行一次原 operation 恢复 |
| `requires_confirmation` | 先取得新的明确确认，再执行 |
| 未知值 | 不执行；展示为不受支持的安全级别，要求人工处理或升级客户端 |

每个 `data.next_commands` 元素必须是包含 `argv` 和 `safety` 的对象；`argv` 必须是非空字符串 token 数组，且所有 token 非空。字段或元素类型不符时将整个信封视为 CLI 契约无效，不执行任何建议。

仅对已知 safety 校验建议 argv 以 `cs-cloud task` 开头、命令存在于当前 catalog，并以 argv 数组直接执行。为获得结构化结果，若建议 argv 不含 `--json`，仅在末尾追加一次；若多于一个则不执行。不要修改其他参数。未知 safety 即使用户确认也不得执行。不要盲目重跑失败的原命令。调用超时且没有结构化建议时，确认原进程已退出后，最多用原始 argv 中已校验的 task key 执行一次由 catalog 识别的只读详情命令；不要重跑原写命令。

## 示例

用户要求“提交任务 X”时，执行 `submit X` 生成预览，展示风险并询问确认。只有用户在看到该预览后明确确认，才执行预览返回的 confirm argv。若结果为 `already_completed`，报告“任务此前已提交”，不要报告“我刚刚重新提交了任务”。

## 红线

- 直接访问 Daemon HTTP/SSE、读取 `server_url`、`local_url` 或 local secret。
- 使用 `curl`、PowerShell Web cmdlet 或另一套任务实现作为回退。
- 用 shell 字符串拼接 `cs-cloud` 参数，或执行 stderr 文本。
- 未实际启动目标命令却只输出计划、命令草稿或模拟成功结果。
- 在展示终态预览的同一阶段替用户确认。
- 将 `previewed`、`observed` 或 `performed=false` 描述为终态完成。
- 失败后盲目重试，或让超时的原进程继续在后台运行。

出现任一红线时停止；保留本地成果，报告已知事实与安全下一步。
