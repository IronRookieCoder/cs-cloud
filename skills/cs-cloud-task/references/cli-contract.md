# cs-cloud task CLI 契约

## Catalog 与 argv

支持 `schema_version` 主版本 `1`。命令仅依赖 `name`、`capability`、`timeout_seconds`、`arguments`、`mutually_exclusive`、`confirmation_mode` 和 `outcomes`。

按 `arguments` 顺序构造 argv：位置参数直接追加；字符串和枚举 flag 使用 catalog 给出的完整 `flag`，按 `flag=value` 传值；值为 true 的布尔 flag 只追加 flag。执行前校验 `required`、`required_when`、`required_unless`、枚举值和互斥组。未知 `kind`、`type`、`value_style`、条件形式或 schema 主版本时停止，不猜测语法。

每次调用只传一个 `--json`，使用 catalog 的超时。以 argv 数组启动已解析的绝对二进制路径，不启用 shell。分别保留 stdout、stderr 和退出码，只解析 stdout；退出码 `2` 后不得换一种参数形式重试。

## 信封

顶层必须是 `{schema_version, ok, data, error}`。成功 `data` 必须包含可验证的 `command`、`performed`、`outcome`、`observed_at`、`result` 和 `next_commands`；有任务的命令还必须有匹配的 `task_key`。退出码必须与 `ok` 一致。缺字段、类型错误、未知枚举、空 stdout 或无效 JSON 都视为契约失败，不执行建议动作。

## 终态确认

首次调用不传 `--confirm`。展示 `data.result` 的完整结构化预览并明确尚未完成，然后单独询问用户。原始请求中的“直接提交”不能代替预览后的确认。

预览必须恰有一个 `safety=requires_confirmation` 的候选。仅在当前会话保存预览、候选 argv 和二进制路径。用户确认后校验：

- argv 是非空字符串数组，以逻辑 `cs-cloud task <command>` 开头；
- 命令存在于当前 catalog，并与预览 `data.command`、`data.task_key` 匹配；
- `--json` 最多出现一次，其余参数可按 catalog 解析；
- 除替换首个可执行文件路径和补充缺失的单个 `--json` 外，不修改任何 token。

预览内容、候选 argv、目标命令变化，或返回 `preview_stale` / `review_snapshot_stale` 时，丢弃旧确认并重新预览。

## 建议动作

每个 `next_commands` 元素必须包含非空 argv 和已知 safety：

| safety | 处理 |
|---|---|
| `observe_only` | 最多自动执行一个只读观察 |
| `preview_only` | 最多执行一个新预览，随后等待确认 |
| `idempotent_recovery` | 先说明，再恢复一次原 operation |
| `requires_confirmation` | 取得新的明确确认后执行 |

未知 safety、重复 `--json`、未知命令或无效 argv 一律不执行。失败后不盲目重跑写命令；超时后先终止并等待原进程退出。

## 禁止事项

- 访问 Daemon HTTP/SSE、配置端口或 local secret；
- 使用 curl、PowerShell Web cmdlet 或内部状态目录回退；
- 拼接 shell 字符串、解析或执行 stderr 文本；
- 把预览、查询、无输出或 `performed=false` 描述为已完成；
- 在展示预览的同一阶段替用户确认。
