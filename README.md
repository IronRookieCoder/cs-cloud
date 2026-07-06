# cs-cloud

> CoStrict 设备端 Cloud Daemon —— 运行在用户设备上的单二进制代理，负责把本地 AI Agent 接入 CoStrict 云端，让"代码不出本地，云端可远程调度"。

---

## 这个项目解决什么问题

客户的代码库不能上传到云端（金融、政企、制造业的强合规要求），但 AI 助手又必须能读取本地代码、执行命令、查看终端。`cs-cloud` 就是这道桥梁：

- 在用户电脑上以守护进程形式常驻运行
- 通过 WebSocket 隧道接入 CoStrict 网关（基于 yamux 多路复用）
- 接收云端下发的指令，本地调度 AI Agent 执行
- 把 Agent 的输出（SSE 事件、终端输出、文件 diff）实时回传到云端

代码、命令执行、终端会话都留在本地，云端只做调度和数据展示。

---

## 核心特性

- **单二进制跨平台**：Linux / Windows / macOS × amd64 / arm64（Windows arm64 除外），CGO_ENABLED=0 静态编译
- **WebSocket 隧道**：yamux 多路复用，单连接承载多路 RPC + 流式数据，支持 keepalive、断线重连、large body 流式传输
- **守护进程生命周期**：start / stop / restart / status，graceful shutdown，孤儿进程清理
- **Agent Runtime 抽象层**：driver 系统 + event bus，支持多种 agent CLI（csc / cs / acp），可配置自定义 env / header
- **终端 PTY 管理**：远程交互式终端，二进制 WebSocket，跨平台 shell 兼容（Windows ConPTY / Unix pty）
- **自动升级**：从 Gitee / GitHub Release 拉取新版本，Windows 用 cmd.exe helper 脚本替换运行中的二进制
- **结构化日志**：zap + lumberjack 滚动归档，按天分文件
- **OAuth 登录与凭证管理**：与 opencode 共享 `~/.costrict/share/auth.json`

---

## 快速开始

### 环境要求

- Go 1.25+
- 操作系统：Linux / Windows / macOS
- 网络可访问 CoStrict 网关（默认 `https://zgsm.sangfor.com`）

### 本地构建与运行

```bash
# 拉取依赖
go mod tidy

# 构建（生成 bin/cs-cloud 或 bin/cs-cloud.exe）
go build -o bin/cs-cloud ./cmd/cs-cloud

# 查看版本
./bin/cs-cloud version

# 登录（写入 ~/.costrict/share/auth.json）
./bin/cs-cloud login

# 检查环境与连接性
./bin/cs-cloud doctor

# 启动守护进程
./bin/cs-cloud start

# 查看运行状态
./bin/cs-cloud status

# 查看日志（follow 模式）
./bin/cs-cloud logs -f

# 停止 / 重启
./bin/cs-cloud stop
./bin/cs-cloud restart
```

### 跨平台 Release 构建

项目使用 goreleaser 跨平台打包：

```bash
# 安装 goreleaser
go install github.com/goreleaser/goreleaser/v2@latest

# 本地测试构建（不发布）
goreleaser release --snapshot --clean
```

构建产物在 `dist/` 目录，覆盖 6 个平台（linux/darwin/windows × amd64/arm64，除 windows-arm64）。

---

## CLI 命令一览

| 命令 | 说明 |
|------|------|
| `cs-cloud login` | OAuth 登录，写入凭证到 `auth.json`，自动触发设备注册 |
| `cs-cloud logout` | 删除 `auth.json` 和 `device.json` |
| `cs-cloud register` | 设备注册，生成 device_id（MAC 地址算法） |
| `cs-cloud start` | 启动后台守护进程（含登录+注册+本地 server） |
| `cs-cloud stop` | 停止守护进程 |
| `cs-cloud restart` | 重启守护进程 |
| `cs-cloud status` | 查看守护进程运行状态与本地 server 地址 |
| `cs-cloud serve` | 前台运行（不带守护进程，调试用） |
| `cs-cloud doctor` | 诊断环境与连接性，输出修复建议 |
| `cs-cloud logs` | 查看日志（支持 `-f` 跟踪） |
| `cs-cloud update` | 检查并应用新版本 |
| `cs-cloud version` | 输出版本号、Commit、Go 版本、平台信息 |

---

## 配置

### auth.json

读取自 `~/.costrict/share/auth.json`（与 opencode 共享）。

支持字段：

| 字段 | 必需 | 说明 |
|------|------|------|
| `access_token` | ✅ | OAuth access token |
| `base_url` | ✅ | CoStrict 平台基址 |
| `refresh_token` | | OAuth refresh token |
| `state` | | OAuth state |
| `machine_id` | | 历史设备标识（已被 device_id 取代） |
| `expiry_date` | | access_token 过期时间 |
| `updated_at` | | 凭证最近更新时间 |
| `expired_at` | | refresh_token 过期时间 |

### device.json

设备注册成功后写入 `~/.costrict/share/device.json`：

- `device_id`
- `device_token`
- `registered_at`
- `base_url`

通过 `cs-cloud register` 触发最小注册流程。

### Cloud Base URL 解析优先级

1. `COSTRICT_CLOUD_BASE_URL` 环境变量
2. `auth.json` 中的 `base_url`
3. `COSTRICT_BASE_URL` 环境变量
4. 默认值 `https://zgsm.sangfor.com`

规则：
- 若结果已以 `/cloud-api` 结尾，直接使用
- 若显式设置了 `COSTRICT_CLOUD_BASE_URL`，不自动追加 `/cloud-api`
- 否则自动补成 `${base}/cloud-api`

### 常用环境变量

| 变量 | 说明 |
|------|------|
| `COSTRICT_CLOUD_BASE_URL` | 强制指定云端基址 |
| `COSTRICT_BASE_URL` | 平台基址（次优先） |
| `CS_CLOUD_AGENT_CLI` | 自定义 agent CLI 路径 |
| `CS_CLOUD_AGENT_PATH` | 多 agent 路径（自动派生命令） |
| `CSC_CLOUD_INVOKER` | 标识调用来源（cloud / cli） |

---

## 登录认证流程

### 完整流程（`cs-cloud login`）

1. 生成 `state` + `machine_id`
2. 构建登录 URL（含 OAuth 参数：`provider=casdoor`、`machine_code`、`state`）
3. 打开浏览器到 CoStrict 登录页
4. 轮询 Token 端点 `/oidc-auth/api/v1/plugin/login/token`，最长 10 分钟
5. 获取 `access_token` + `refresh_token`
6. 保存到 `~/.costrict/share/auth.json`
7. 自动触发设备注册

### 启动时自动登录（`cs-cloud start`）

1. 检查是否已在运行
2. 尝试设备注册（`/api/devices/register`）
3. 如果认证缺失 / 过期 → 自动触发浏览器登录
4. 登录完成后重试注册
5. 验证设备 Token（`/cloud/device/gateway-assign`）
6. 如果 Token 无效 → 清除本地设备 → 重新注册 → 重新验证
7. 启动本地 server

### Token 刷新策略

注册时如果 `access_token` 过期，按三层策略判断：

1. `expiry_date`（30 分钟缓冲）
2. `refresh_token` JWT 解析
3. `access_token` JWT 解析（30 分钟缓冲）

刷新使用 `refresh_token` 调用 `/oidc-auth/api/v1/plugin/login/token`（不含 `machine_code`）。

---

## 本地 Control Plane（Local Server）

`start` 命令会拉起一个本地 HTTP control plane，供云端隧道访问。

### 当前提供的接口

- `GET /health` —— 健康检查（含 tunnel 状态）
- `GET /agents` —— 已注册 agent 列表
- `GET /runtime/health` —— Runtime 健康检查
- `GET /runtime/vcs` —— Git 仓库状态信息
- `GET /runtime/diff` —— 文件 diff（取代旧的 conversation diff）
- `GET /init-status` —— Prewarm 初始化状态
- `POST /runtime/update/apply` —— 触发自动升级

完整 API 列表参见 `internal/localserver/apidocs/`（基于 Swagger 自动生成）。

### 生命周期管理

- `start`：后台启动（含登录+注册+本地 server）
- `serve`：前台运行（调试用）
- `stop` / `restart` / `status`：守护进程控制
- `doctor`：完整诊断

---

## 常见问题 FAQ

### Windows 上启动后报"系统找不到指定的文件"

确保 `auth.json` 已生成（先执行 `cs-cloud login`）。Windows 路径区分大小写，建议用 `%USERPROFILE%` 而非相对路径。

### Linux 上 `cs-cloud start` 卡住

通常是 OAuth token 过期或网关不可达。先执行 `cs-cloud doctor` 排查，它会检查：
- auth.json 是否存在且有效
- 网关连通性
- 进程状态
- 日志文件可读性

### 设备 ID 冲突（克隆机器导致）

device_id 基于 MAC 地址生成。如果虚拟机克隆后出现冲突，cs-cloud 会自动检测并触发设备克隆恢复流程（保留旧 device_id 兼容性，同时生成新 fingerprint）。

### 自动升级后 binary 没替换成功（Windows）

Windows 下正在运行的 binary 无法被覆盖。cs-cloud 通过 `cmd.exe` helper 脚本实现：等待主进程退出 → 替换 binary → 重启。如果仍然失败，手动 `cs-cloud stop` 后重新升级。

### 日志在哪

- **Linux / macOS**: `~/.costrict/cs-cloud/logs/app.log`
- **Windows**: `%USERPROFILE%\.costrict\cs-cloud\logs\app.log`

按天滚动，保留最近 10 个文件。请求日志独立到 `requests.log`。

---

## 文档

详细设计文档在 `docs/` 目录：

- [`agent-runtime-definition.md`](docs/agent-runtime-definition.md) —— Agent Runtime 抽象层设计
- [`cloud-terminal.md`](docs/cloud-terminal.md) —— 云终端 PTY 实现方案
- [`runtime-control-api.md`](docs/runtime-control-api.md) —— Runtime 控制 API 规范
- [`versioning-and-upgrade.md`](docs/versioning-and-upgrade.md) —— 版本管理与自动升级
- [`cli-interaction-proposal.md`](docs/cli-interaction-proposal.md) —— CLI 交互设计
- [`multi-agent-support.md`](docs/multi-agent-support.md) —— 多 Agent 支持
- [`api-data-structures.md`](docs/api-data-structures.md) —— API 数据结构
- [`cloud-command-pipeline.md`](docs/cloud-command-pipeline.md) —— 云命令管道

架构总览见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

---

## 贡献

- 提交 PR 前请确保 `go vet ./...` 通过
- 涉及 API 变更需要更新 `docs/` 下相关文档
- 跨平台变更至少在 Linux + Windows 各验证一次

## 许可证

见仓库 LICENSE。
