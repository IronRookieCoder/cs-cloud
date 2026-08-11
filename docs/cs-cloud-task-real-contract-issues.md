# cs-cloud task 正式模式契约问题记录

## 1. 范围

本文只记录不依赖 fixture、在正式 Cloud Daemon 流程中同样存在的 `cs-cloud task` 公共契约问题。fixture 材料目录未落盘、固定响应不推进状态等测试模式问题不在本文范围内。

## 2. 任务列表缺少可展示名称

### 现象

`cs-cloud task list --json` 返回任务标识、角色、远端版本和投影状态，但没有任务名称、Issue 标题、节点名称或目标摘要。用户查看待办列表时只能看到 workspace、node run ID 和角色，无法快速识别任务内容。

### 根因

`membertask.Service.List` 合并本地记录和 `RemoteTask` 后直接返回 `Task`，不会获取 `RemoteTaskContext`。当前 `RemoteTask` 也没有稳定的展示名称字段。

### 影响

- CLI、skill 和其他调用方无法在列表视图展示有业务含义的任务名称。
- 调用方必须逐项执行 `get` 才能取得 `goal`，任务较多时会产生额外请求和延迟。
- fixture 即使补充名称，也会与正式模式的返回契约产生差异，不能解决正式问题。

### 建议

在任务列表服务端契约中增加稳定的只读展示字段，例如 `display_name`、`issue_title` 和 `node_name`；CLI 直接透传，不通过解析 `goal` 临时生成名称。新增字段应保持向后兼容。

## 3. 准备结果缺少材料来源与 PR 信息

### 现象

正式 `prepare` 返回 `LocalTransition`。其中的 `TaskRecord` 可以包含本地目录和 manifest，但无法表达材料原始 URL、Gitea PR URL 或交付物标题。调用方只能依赖准备前的 `get.context`，无法仅根据准备结果说明材料来自哪个 PR。

### 根因

- `TaskRecord` 和 `Manifest` 关注本地一致性、文件摘要及仓库基线，没有材料来源 URL。
- `RemoteDeliverable.url` 只存在于远端上下文，不会进入准备后的本地材料 manifest。

### 影响

- 准备结果无法独立展示“本地目录 + PR + commit + 材料路径”的完整信息。
- 新会话或未缓存 `get` 结果的调用方需要再次查询远端上下文。
- 离线场景中，本地任务记录不能还原材料的外部来源地址。

### 建议

在本地材料 manifest 中保存非敏感、不可变的来源描述，例如交付物标题、source URL、provider、repository identity 和 commit SHA；或为 `prepare` 结果增加统一的材料摘要。应明确 URL 的信任边界，禁止将凭据写入本地记录。

## 4. 验收建议

- `task list` 的每个远端任务都包含稳定的业务展示名称。
- `task prepare` 的结果可同时定位本地目录、具体材料文件和原始 PR。
- 断网后执行 `task get` 仍可从本地记录展示已准备材料的来源摘要。
- 老版本服务端缺少新增字段时，CLI 保持兼容并明确降级展示。
