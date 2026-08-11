# cs-cloud-task Fixture 演示与正式测试指南

## 1. 目的

本文说明如何使用本地 fixture 演示 `cs-cloud-task` skill 的完整审查流程，以及如何对 fixture 参数、CLI JSON 契约和两阶段审查确认执行正式测试。

fixture 模式只返回本地测试数据，不连接 Multica，不修改线上 Issue、Workflow、Gitea PR 或本地任务目录。未传 `--fixture` 时，`cs-cloud task` 仍按正式逻辑连接 Cloud Daemon。

## 2. 演示数据

测试数据文件：

```text
F:\ai-coding\cs-cloud\skills\cs-cloud-task\testdata\costrict-006485da-solution-review.json
```

稳定任务标识：

```text
zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic
```

数据取自线上页面：

```text
https://zgsm.sangfor.com/cloud/workflow/costrict/issues/006485da-3f92-443e-8c5d-677bfbd82225
```

关键事实：

| 字段 | 值 |
|---|---|
| Issue | `COS-155 用原生web技术创建一个五子棋游戏-2100` |
| Workflow 节点 | `方案设计` |
| 执行者 | `方案设计师` |
| 审查者 | `IronRookieCoder` |
| 交付物 | `五子棋游戏方案设计文档` |
| 文件 | `gomoku-solution-design.md`，133 行新增 |
| Gitea PR | `https://git-common-zgsm.sangfor.com/t-a74c21e4/wf-b7d47227/pulls/3` |
| 源分支 | `node/01-0447d3a1` |
| 目标分支 | `inst-97cf6dd2` |
| 不可变 commit | `b015f800da59abd2cfdb3795de8201b449bcc982` |

fixture 中的 preview ID、operation ID、review snapshot ID 和 SHA-256 digest 是离线演示标识，不是线上数据库主键。

## 3. 环境准备

### 3.1 构建 CLI

在 PowerShell 中执行：

```powershell
Set-Location F:\ai-coding\cs-cloud
go build -o "$env:TEMP\cs-cloud-fixture-demo.exe" ./cmd/cs-cloud
```

正式安装到当前 CoStrict 用户目录时，先确认 Daemon 未运行：

```powershell
& 'C:\Users\demo\.costrict\bin\cs-cloud.exe' status --json
```

当输出包含 `"running": false` 后再安装：

```powershell
go build -o 'C:\Users\demo\.costrict\bin\cs-cloud.exe' ./cmd/cs-cloud
node scripts\install-cs-cloud-task-skill.mjs
```

安装后的 skill 路径应为：

```text
C:\Users\demo\.costrict\skills\cs-cloud-task\SKILL.md
```

若 CoStrict/CSC 已经打开，需要重启对应会话，使其重新发现 skill 和 CLI。

### 3.2 检查 catalog

```powershell
$binary = 'C:\Users\demo\.costrict\bin\cs-cloud.exe'
& $binary task help --json
```

检查以下内容：

- 顶层 `schema_version` 为 `1.0`。
- `commands` 包含 `list`、`get`、`prepare`、`start`、`pause` 和 `review`。
- 上述可执行命令的 `arguments` 包含 `--fixture`。
- `review.confirmation_mode` 为 `preview_then_confirm`。
- `review` 的 `decision` 只允许 `approve` 或 `reject`。

## 4. Skill 交互演示

### 4.1 测试数据注入与交互边界

交互演示只模拟后台返回的数据，用户操作必须按真实使用场景逐步进行。测试数据通过临时环境变量传给 CLI，不作为用户消息发送，也不出现在 skill 面向用户的回复中。

用户消息中不得出现 `演示`、`fixture`、`task_key`、`cs-cloud-task skill`、测试步骤编排或预期返回值。不得用一条提示词要求 skill 连续完成整个流程；每一步都由用户在看到上一轮真实格式的结果后再发起。

具体操作步骤：

1. 按 3.1 节构建并安装待测 CLI 和 skill，完全退出安装前已打开的 CSC/CoStrict 进程。
2. 在 PowerShell 中进入仓库并设置当前进程级临时环境变量：

   ```powershell
   Set-Location F:\ai-coding\cs-cloud
   $env:CS_CLOUD_TASK_FIXTURE = (Resolve-Path 'skills\cs-cloud-task\testdata\costrict-006485da-solution-review.json').Path
   ```

3. 在同一个 PowerShell 中验证 CLI 已读取测试数据，命令中不要传 `--fixture`：

   ```powershell
   $binary = 'C:\Users\demo\.costrict\bin\cs-cloud.exe'
   & $binary task list --json
   ```

   输出应包含任务标识 `zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic`。如果命令仍尝试连接 Daemon，说明安装的 CLI 版本不支持该环境变量，应重新执行 3.1 节的构建安装。
4. 从这个 PowerShell 启动 CSC/CoStrict，使新进程继承 `CS_CLOUD_TASK_FIXTURE`。不要从已经运行的桌面进程中继续旧会话，也不要另开未设置该变量的终端启动。
5. 创建新会话，按 4.2 节逐条发送自然业务指令。skill 仍执行标准 `cs-cloud task` 命令，不需要知道 fixture 路径；任务标识从 `list` 的结果中取得。用户明确开始处理尚未准备的任务时，skill 在同一轮后台先执行 `prepare`，成功后继续执行 `start`。
6. 首次 `review` 返回预览后停止。用户在下一轮明确确认时，skill 直接执行 `data.next_commands` 中唯一的 `safety=requires_confirmation` argv；CLI 会在该 argv 中保留 fixture 参数。
7. 每轮只检查并记录本轮结构化结果，再发送下一条业务指令。面向用户的回复中不得泄露 fixture 路径、模拟标识或测试控制逻辑。
8. 完成一个场景后关闭该会话。需要执行另一个审查场景时，新建会话以获得初始 fixture 状态。
9. 全部测试结束后，在启动 CSC/CoStrict 的 PowerShell 中清除临时环境变量：

   ```powershell
   Remove-Item Env:CS_CLOUD_TASK_FIXTURE
   ```

   清除后新启动的 CLI 和 CSC/CoStrict 将恢复正式 Daemon 流程。已启动的子进程仍保留其环境副本，需要一并退出。

### 4.2 逐步交互流程

#### 第一步：发现待办任务

用户输入：

```text
查看一下我当前需要处理的任务。
```

skill 应列出当前任务。受 [正式模式任务列表契约问题](./cs-cloud-task-real-contract-issues.md#2-任务列表缺少可展示名称) 影响，列表阶段只能根据唯一的 `critic` 任务及其稳定 task key 选择任务，不能声称列表已经返回 Issue 名称。

#### 第二步：查看任务详情

用户输入：

```text
查看这个审查任务的详细信息。
```

skill 应通过 `get` 展示 `COS-155 用原生web技术创建一个五子棋游戏-2100`、节点、执行者、审查者、交付物、PR、commit、验收条件和设计范围，不推进任务状态。

#### 第三步：开始处理

用户输入：

```text
开始处理这个任务。
```

skill 发现任务为 `not_prepared` 后，应在本轮后台完成以下操作：

1. 执行 `prepare`，确认材料准备成功并取得绝对本地目录。
2. 检查 `<directory>\review\gomoku-solution-design.md` 可读取。
3. 执行 `start`，将任务展示状态推进为 `in_progress`。

skill 面向用户汇总“材料已准备并开始处理”，可展示本地材料文件、PR 和 commit，但不要要求用户单独确认准备操作。任一后台操作失败时立即停止，不得声称任务已开始。

#### 第四步：提交审查决定

用户在审阅交付物后，根据通过或驳回场景发送对应的业务指令。skill 必须先生成审查预览并停止，不能在同一轮中代替用户确认。

完整流程的状态变化如下：

| 步骤 | CLI outcome | performed | 展示状态 |
|---|---|---:|---|
| 查看列表 | `observed` | `false` | 查询完成，不推进状态 |
| 查看详情 | `observed` | `false` | `not_prepared` |
| 开始处理：后台准备 | `completed` | `true` | `prepared` |
| 开始处理：激活任务 | `completed` | `true` | `in_progress` |
| 生成审查预览 | `previewed` | `false` | 等待用户确认 |
| 用户确认 | `completed` | `true` | 审查操作完成 |

详情中应展示：

- 当前任务是“成果审查”，不是首次执行或返工。
- Issue、节点、执行者和审查者信息。
- 交付物标题、PR 地址和 133 行新增。
- 不可变 commit SHA。
- 验收条件和设计范围。

### 4.3 通过审查场景

用户输入：

```text
方案设计符合验收要求，通过这个审查。
```

skill 展示通过预览后，应明确说明操作尚未完成。预览至少应包含：

- 摘要：通过五子棋游戏方案设计文档。
- 目标：Multica 方案设计节点审查结论。
- 影响：通过后进入任务拆解阶段。
- PR：`t-a74c21e4/wf-b7d47227#3`。
- commit：`b015f800da59abd2cfdb3795de8201b449bcc982`。
- manifest digest：`bb349760c4bc7a59abf0e564a59bdd1875f3da341338c38c7201494e401c7c71`。

确认预览内容后，用户在下一轮单独回复：

```text
确认通过。
```

skill 只能执行预览信封中唯一的 `safety=requires_confirmation` 候选 argv，不得自行重构 `--confirm`、decision、fixture 路径或其他参数。

预期结果：

```text
outcome=completed
performed=true
kind=approve
operation=costrict-006485da-operation-approve
```

### 4.4 驳回审查场景

使用全新的会话和初始测试数据，按 4.2 的第一步至第三步完成任务发现与处理。用户审阅交付物后输入：

```text
这个方案需要补充键盘可访问性验收说明，请驳回。
```

skill 应从用户消息中提取驳回原因；原因不明确时应先询问，不能启动 review 命令。预览生成后，用户在下一轮单独回复：

```text
确认驳回。
```

预期结果：

```text
outcome=completed
performed=true
kind=reject
operation=costrict-006485da-operation-reject
```

## 5. CLI 手工验证

### 5.1 初始化变量

```powershell
$binary = 'C:\Users\demo\.costrict\bin\cs-cloud.exe'
$fixture = (Resolve-Path 'F:\ai-coding\cs-cloud\skills\cs-cloud-task\testdata\costrict-006485da-solution-review.json').Path
$fixtureArg = "--fixture=$fixture"
$taskKey = 'zgsm/costrict/006485da-3f92-443e-8c5d-677bfbd82225/critic'
```

### 5.2 查看列表与详情

```powershell
& $binary task list $fixtureArg --json
& $binary task get $taskKey $fixtureArg --json
```

验收：

- 两个命令退出码均为 `0`。
- `ok=true`、`outcome=observed`、`performed=false`。
- `get.data.result.context.goal` 包含 `COS-155`。
- 交付物 URL 为 PR#3。
- repository `head_sha` 为完整 commit SHA。

### 5.3 准备、开始和暂停

```powershell
$prepareResult = & $binary task prepare $taskKey $fixtureArg --json | ConvertFrom-Json
& $binary task start $taskKey $fixtureArg --json
& $binary task pause $taskKey $fixtureArg --json
$materialPath = Join-Path $prepareResult.data.result.directory 'review\gomoku-solution-design.md'
Get-Content -Raw -Encoding utf8 $materialPath
```

验收：

- 三个命令均返回 `ok=true`、`outcome=completed`、`performed=true`。
- `prepare.state_after.display_status=prepared`。
- `start.state_after.display_status=in_progress`。
- `pause.state_after.display_status=prepared`。
- `prepare.data.result.directory` 是绝对路径，且 `review\gomoku-solution-design.md` 存在并可读取。

### 5.4 通过预览与确认

先只生成预览：

```powershell
$approvePreview = & $binary task review $taskKey '--decision=approve' $fixtureArg --json | ConvertFrom-Json
$approvePreview.data.result | ConvertTo-Json -Depth 10
$approvePreview.data.next_commands | ConvertTo-Json -Depth 10
```

人工检查预览后，验证候选数量和安全级别：

```powershell
if ($approvePreview.data.next_commands.Count -ne 1) { throw 'confirmation candidate must be unique' }
if ($approvePreview.data.next_commands[0].safety -ne 'requires_confirmation') { throw 'unsafe confirmation candidate' }
```

确认阶段必须复用返回的 argv，只替换逻辑可执行文件名：

```powershell
$approveArgs = @($approvePreview.data.next_commands[0].argv | Select-Object -Skip 1)
$approveResult = & $binary @approveArgs --json | ConvertFrom-Json
$approveResult.data | ConvertTo-Json -Depth 10
```

验收：`outcome=completed`、`performed=true`、`result.kind=approve`。

### 5.5 驳回预览与确认

```powershell
$rejectPreview = & $binary task review $taskKey '--decision=reject' '--reason=方案需补充键盘可访问性验收说明' $fixtureArg --json | ConvertFrom-Json
$rejectPreview.data.result | ConvertTo-Json -Depth 10
$rejectPreview.data.next_commands | ConvertTo-Json -Depth 10
```

人工检查预览后执行候选：

```powershell
if ($rejectPreview.data.next_commands.Count -ne 1) { throw 'confirmation candidate must be unique' }
if ($rejectPreview.data.next_commands[0].safety -ne 'requires_confirmation') { throw 'unsafe confirmation candidate' }
$rejectArgs = @($rejectPreview.data.next_commands[0].argv | Select-Object -Skip 1)
$rejectResult = & $binary @rejectArgs --json | ConvertFrom-Json
$rejectResult.data | ConvertTo-Json -Depth 10
```

验收：`outcome=completed`、`performed=true`、`result.kind=reject`。

## 6. 自动化正式测试

### 6.1 相关模块测试

按项目约定只运行相关模块：

```powershell
Set-Location F:\ai-coding\cs-cloud
go test ./internal/cli -count=1
```

预期：

```text
ok cs-cloud/internal/cli
```

### 6.2 Fixture 专项测试

```powershell
go test ./internal/cli -run 'TestTaskFixture' -count=1
```

覆盖内容：

- catalog 为所有可执行 task 命令声明 `--fixture`。
- `get` 返回指定 Issue、交付物、PR 和 commit。
- `prepare` 把 fixture 相对材料目录解析为绝对路径，声明的材料文件存在且可读。
- 绝对材料路径、越界路径和缺失材料文件被拒绝。
- 审查预览的确认 argv 保留原 fixture 路径。
- 通过和驳回确认绑定各自 preview ID。
- 确认结果返回正确 decision、operation 和完成状态。

### 6.3 构建验证

```powershell
go build -o "$env:TEMP\cs-cloud-fixture-demo.exe" ./cmd/cs-cloud
& "$env:TEMP\cs-cloud-fixture-demo.exe" task help --json
```

验收：构建退出码为 `0`，catalog 可被解析为 JSON。

### 6.4 JSON 与 diff 检查

```powershell
Get-Content -Raw -Encoding utf8 skills\cs-cloud-task\testdata\costrict-006485da-solution-review.json | ConvertFrom-Json | Out-Null
git diff --check
git status --short
```

验收：

- fixture JSON 可正常解析。
- `git diff --check` 无空白错误。
- 变更范围只包含 fixture 功能、相关测试、测试数据和本文档。

## 7. 负向测试

### 7.1 驳回缺少原因

```powershell
& $binary task review $taskKey '--decision=reject' $fixtureArg --json
$LASTEXITCODE
```

预期：退出码 `2`，`ok=false`，错误码为 `invalid_arguments`，未生成预览。

### 7.2 Task key 不匹配

```powershell
& $binary task get 'zgsm/costrict/other-node/critic' $fixtureArg --json
$LASTEXITCODE
```

预期：非零退出，`ok=false`，错误码为 `invalid_fixture`。

### 7.3 无效确认 ID

```powershell
& $binary task review $taskKey '--confirm=unknown-preview' $fixtureArg --json
$LASTEXITCODE
```

预期：非零退出，`ok=false`，不会回退到其他确认结果。

### 7.4 重复 fixture 参数

```powershell
& $binary task get $taskKey $fixtureArg $fixtureArg --json
$LASTEXITCODE
```

预期：退出码 `2`，错误码为 `invalid_arguments`，不会读取 fixture。

### 7.5 不传 fixture

不传 `--fixture` 时会走真实 Daemon。为了避免误操作，正式环境中只用只读命令验证回退：

```powershell
& $binary task list --json
```

不得在没有明确测试账号、测试任务和用户确认时执行真实 `submit`、`review` 或 `delete`。

## 8. 验收清单

- [ ] catalog schema 主版本为 `1`。
- [ ] `--fixture` 由 catalog 声明，不依赖 skill 猜测参数。
- [ ] fixture task key 与请求严格匹配。
- [ ] 列表和详情不推进业务状态。
- [ ] 准备、开始、暂停返回预期状态。
- [ ] 通过和驳回都先预览、后独立确认。
- [ ] 确认 argv 保留 task key、preview ID 和 fixture 路径。
- [ ] 预览绑定 PR、完整 commit SHA 和 manifest digest。
- [ ] `performed=false` 不被描述为已完成。
- [ ] fixture 模式不连接 Multica、Gitea 或 Daemon。
- [ ] 未传 fixture 时保持原正式流程。
- [ ] 相关模块测试、构建和 JSON 校验通过。
