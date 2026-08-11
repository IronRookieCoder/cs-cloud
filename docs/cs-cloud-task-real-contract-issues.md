# cs-cloud task 正式模式契约问题修复记录

## 修复范围

本文记录正式 Cloud Daemon 流程中的任务展示、准备目录和材料来源契约。fixture 的固定响应行为不在范围内。

## 任务列表展示

`RemoteTask` 现可接收向后兼容的 `display_name`、`issue_title` 和 `node_name`。本地 `Task` 统一返回：

- `display_name`：供 CLI 和 skill 直接展示；
- `display_name_source`：名称来源，可能为 `cloud`、`issue_title`、`node_name`、`local` 或 `task_identity`。

名称优先级为云端展示名、Issue 标题、节点名称、本地缓存、任务标识。旧服务端缺少新字段时降级为 `workspace/node/role`，不会逐项请求 `get`，因此列表仍为单次云端请求。

## 当前目录准备

`prepare` 新增可选参数：

```text
cs-cloud task prepare <task-key> --workdir=<path> --json
```

CLI 将相对路径规范化为绝对路径，本地服务仅接受已存在的绝对目录。材料写入：

```text
<workdir>/.cs-cloud-tasks/<cloud>/<workspace>/<node-role>/
```

未传 `--workdir` 时继续使用原用户配置目录。任务记录、准备日志和删除日志会保存受管根目录；已有任务位于其他目录时返回 `prepare_location_conflict`，不自动移动或覆盖。

## 材料来源与 PR 信息

文件和仓库 manifest 可保存 `origin`：标题、无凭据来源 URL、provider、仓库标识、commit SHA 和材料相对路径。URL 仅保留有效 HTTPS 地址，并移除用户信息、查询参数和 fragment。

`prepare` 结果通过 `TaskRecord.manifest` 返回来源摘要；记录落盘后，断网执行 `task get` 仍能读取相同信息。云端未提供某项来源字段时保持为空，不从目标描述中猜测。

## Skill 交互

- 主 skill 只保留快速路径和输出契约，低频 CLI 校验移入按需 reference。
- 明确意图只读取对应命令 catalog，并在当前会话缓存 catalog、二进制路径和序号映射。
- 列表固定展示序号、任务描述、角色、状态和可操作项。
- 列表后提供 `详情 1`、`准备 1`、`开始 1`、`刷新` 等中文快捷命令。
- 默认不向用户回显原始命令、JSON、stderr 或二进制路径；终态预览和独立确认规则保持不变。

## 验收标准

- 新服务端展示字段可直接透传；旧服务端有明确、无额外请求的降级显示。
- `prepare --workdir=.` 在当前目录的 `.cs-cloud-tasks` 下创建任务专属目录。
- 准备结果可以同时定位本地目录、材料路径和可用的原始来源。
- 离线 `get` 可读取已持久化的名称和材料来源。
- skill 列表具有序号、描述、状态、可操作项和明确快捷下一步。
