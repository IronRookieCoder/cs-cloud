# cs-cloud task CLI 契约

## Catalog 与 argv

支持 `schema_version` 主版本 `1`。命令仅依赖 `name`、`capability`、`timeout_seconds`、`arguments`、`mutually_exclusive`、`confirmation_mode` 和 `outcomes`。

按 `arguments` 顺序构造 argv：位置参数直接追加；字符串和枚举 flag 使用 catalog 给出的完整 `flag`，按 `flag=value` 传值；值为 true 的布尔 flag 只追加 flag。执行前校验 `required`、`required_when`、`required_unless`、枚举值和互斥组。未知 `kind`、`type`、`value_style`、条件形式或 schema 主版本时停止，不猜测语法。

`task_key` 的 CLI 位置参数是字符串 `cloud_instance_id/workspace_id/node_run_id/role`，由列表返回对象的四个字段按此顺序拼接；不要传 JSON 对象、`--task-key` flag、截断值、冒号分隔值或本地目录路径。传参前先用同一字符串做四段校验，校验失败不启动 CLI。

每次调用只传一个 `--json`，使用 catalog 的超时。以 argv 数组启动已解析的绝对二进制路径，不启用 shell。分别保留 stdout、stderr 和退出码，只解析 stdout；退出码 `2` 后不得换一种参数形式重试。禁止使用 `2>&1`、管道合并或把 stderr 重定向到 stdout；stderr 只用于诊断，不得参与 JSON、状态或成功判断。

## 信封

顶层必须是 `{schema_version, ok, data, error}`。成功 `data` 必须包含可验证的 `command`、`performed`、`outcome`、`observed_at`、`result` 和 `next_commands`；有任务的命令还必须有匹配的 `task_key`。退出码必须与 `ok` 一致。缺字段、类型错误、未知枚举、空 stdout 或无效 JSON 都视为契约失败，不执行建议动作。

序号快捷操作必须先绑定当前 `list` 返回的完整 `task_key`。执行后将请求绑定的 `task_key` 与返回的 `data.task_key` 做完全匹配（大小写和分隔符均不得改写）；缺失或不一致时按契约失败处理，停止任何 `next_commands`，先重新获取 `list`。不得使用 display_name、workspace、截断 UUID 或上一轮 task_key 代替完整匹配。

展示序号只在过滤、排序、去重后的最终列表上重新编号；CLI 原始数组下标不是用户序号。任何刷新或过滤都会使旧序号失效，除非当前绑定仍能完全匹配同一 `task_key`。

## 终态确认

首次调用不传 `--confirm`。展示 `data.result` 的完整结构化预览并明确尚未完成，然后单独询问用户。原始请求中的“直接提交”不能代替预览后的确认。若提交预览没有任何文件和仓库，必须明确标为“空交付”，普通“提交/确认”不视为有效确认；只有用户明确确认空交付才可继续。

预览必须恰有一个 `safety=requires_confirmation` 的候选。仅在当前会话保存预览、候选 argv 和二进制路径。用户确认后校验：

- argv 是非空字符串数组，以逻辑 `cs-cloud task <command>` 开头；
- 命令存在于当前 catalog，并与预览 `data.command`、`data.task_key` 匹配；
- `--json` 最多出现一次，其余参数可按 catalog 解析；
- 除替换首个可执行文件路径和补充缺失的单个 `--json` 外，不修改任何 token。

预览内容、候选 argv、目标命令变化，或返回 `preview_stale` / `review_snapshot_stale` 时，丢弃旧确认并重新预览。

## member-task 提交参数

worker 的本地文件交付使用 `cs-cloud task submit <task_key> --deliverable <id> --file <path>`。`--deliverable` 与紧随其后的 `--file` 构成一个绑定，可重复多次；每个 deliverable 只能绑定一次。`--confirm <preview_id>` 仅用于确认已有 preview，不能和任何绑定参数同时出现。

绑定文件必须位于准备任务目录内，且为普通文件；禁止目录逃逸、符号链接、`task.json` 和 `manifest.json`。路径校验失败使用 `deliverable_binding_invalid`。只有显式绑定的文件才会进入 `data.result.files` 和 publish manifest，未绑定的普通文件不会被自动扫描。

服务端 manifest 文件字段包括 `deliverable_id`、`version`、`name`、`relative_path`、`sha256`、`content`。其中 `name` 必须是安全 basename，`relative_path` 用于审计，`version` 必须与内容摘要一致（`sha256:<digest>`）；单文件最大 1 MiB，总文件内容最大 4 MiB。缺失 required file 使用 `required_deliverable_missing`。

材料准备失败时，正文不可用使用 `material_content_unavailable`；已落盘的 reference-only 材料摘要变化使用 `input_material_modified`。网络中断或确认响应丢失时保留本地结果，使用原 operation 执行恢复，不创建新的 submission。

## 建议动作

每个 `next_commands` 元素必须包含非空 argv 和已知 safety：

| safety | 处理 |
|---|---|
| `observe_only` | 最多自动执行一个只读观察 |
| `preview_only` | 最多执行一个新预览，随后等待确认 |
| `idempotent_recovery` | 先说明，再恢复一次原 operation |
| `requires_confirmation` | 取得新的明确确认后执行 |

未知 safety、重复 `--json`、未知命令或无效 argv 一律不执行。失败后不盲目重跑写命令；超时后先终止并等待原进程退出。

调用失败处理：同一用户请求的首次调用若返回退出码 `2`、`invalid_arguments`、`invalid_task_key` 或参数解析错误，立即终止该请求；不得改用另一种参数形式、追加 `--help`、猜测分隔符、搜索内部目录或重复调用。只读命令也适用此规则。只有拿到新的可信输入（用户明确修正、重新获取的 catalog/契约，或 CLI 返回的 `next_commands`）后，才可发起新的调用；新的调用必须作为新请求重新校验。

## 禁止事项

- 访问 Daemon HTTP/SSE、配置端口或 local secret；
- 使用 curl、PowerShell Web cmdlet 或内部状态目录回退；
- 拼接 shell 字符串、解析或执行 stderr 文本；
- 使用 `2>&1`、管道或其它方式合并 stdout 与 stderr；
- 把预览、查询、无输出或 `performed=false` 描述为已完成；
- 在展示预览的同一阶段替用户确认。
