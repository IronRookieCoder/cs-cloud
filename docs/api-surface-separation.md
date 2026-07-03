# API Surface Separation & Agent Routing Design

## 核心原则

cs-cloud 有双重身份：

| 身份 | 职责 | API 前缀 | 处理方 |
|---|---|---|---|
| **Runtime 服务者** | 文件、终端、VCS 等 | `/api/v1/runtime/*` | cs-cloud 自身 |
| **Agent 路由者** | 对话、权限、事件、模型切换等 | `/api/v1/agents/*` | 路由到目标 agent runtime |

上游消费者（app-ai-native）只需知道一个 base URL，通过路径前缀区分请求归属。

### 术语区分：Session Mode vs Agent Runtime

这两个概念容易混淆，必须明确区分：

| 概念 | Session Mode | Agent Runtime |
|---|---|---|
| **定义** | 同一 runtime 内的对话模式 | 独立的 agent CLI 进程 |
| **示例** | build, plan, general, explore | claude, qwen, goose, codex |
| **差异维度** | 权限规则 + system prompt + 温度 | 通信协议 + CLI 命令 + 认证方式 |
| **数量关系** | 一个 runtime 可有多种 session mode | 多个 runtime 可并行运行 |
| **API 归属** | `/agents/session-modes`（X-Backend 可选）+ 创建对话时 `session_mode` 字段 | `X-Backend` header（可选）+ `/agents` 列表 |
| **opencode 对应** | `/agent` 接口返回的 `Agent.Info[]` | 无（opencode 自身就是唯一 runtime） |

**opencode 的 `/agent` 接口**：返回 `Agent.Info[]`，每项包含 `name`（如 "build"）、`mode`（"primary"/"subagent"）、`permission`、`prompt` 等。这是 **session mode 选择**，不是 runtime 选择。

**cs-cloud 的 `/agents` 接口**：返回 `RuntimeStatus[]`，每项包含 `id`（如 "claude"）、`driver`（"acp"）、`available`、`resolved_path` 等。这是 **runtime 选择**。

**cs-cloud 的 `/agents/session-modes` 接口**：返回指定 runtime（通过 `X-Backend` header 选择，省略则用默认）支持的会话模式列表。

在 cs-cloud 中，创建对话时两者需要同时指定：
```
POST /conversations
X-Backend: claude          ← 选择 Agent Runtime
{ "session_mode": "build" } ← 选择 Session Mode
```

---

## API 分区总览

```
/api/v1/
├── runtime/              ← cs-cloud 自身消费
│   ├── health
│   ├── target/context
│   ├── files
│   ├── files/content
│   ├── find/files
│   ├── find/text
│   ├── vcs
│   └── terminals/*
│
├── agents/               ← Agent 路由层（X-Backend 可选，省略则用默认 runtime）
│   ├──                   列出可用 agent runtime
│   ├── health            健康检查
│   ├── models            模型信息
│   ├── session-modes     会话模式列表
│   ├── config            配置读写
│   ├── mcp/*             MCP 连接管理
│   └── lsp/status        LSP 状态查询
│
├── conversations/        ← Agent 路由层（需 X-Backend）
│   ├──                   POST 创建 / GET 列表
│   └── :id/*             对话操作（prompt, abort, messages, diff...）
│
├── interactions/         ← Agent 路由层（需 X-Backend）
│   ├── permissions/*
│   └── questions/*
│
└── events/stream         ← Agent 路由层（WebSocket，X-Backend 过滤）
```

---

## 请求处理流程

### Runtime API — cs-cloud 直接处理

```
Request → /api/v1/runtime/files?path=/home
  → AuthMiddleware
  → RuntimeHandler (cs-cloud 自身逻辑)
  → Response
```

无 `X-Backend` 要求，cs-cloud 独立完成。

### Agent API — 路由到目标 Agent Runtime

```
Request → /api/v1/conversations (X-Backend: claude)
  → AuthMiddleware
  → AgentRoutingMiddleware    ← 解析 X-Backend/X-Driver，注入 context
  → AgentHandler              ← 从 AgentManager 取 Agent，转发请求
  → Agent.SendMessage(...)    ← ACPAgent 通过 stdio JSON-RPC 下发
  → Adapter.TranslateEvent()  ← 将 ACP 响应转为统一格式
  → Response
```

---

## AgentRoutingMiddleware

```go
// internal/localserver/middleware.go

type contextKey string

const (
    ctxKeyBackend contextKey = "backend"
)

func (s *Server) agentRoutingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        backend := r.Header.Get("X-Backend")
        if backend == "" {
            backend = s.registry.DefaultBackend()
        }

        ctx := context.WithValue(r.Context(), ctxKeyBackend, backend)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

---

## Server 路由注册

```go
// internal/localserver/server.go

func New(opts ...Option) *Server {
    s := &Server{}
    for _, o := range opts {
        o(s)
    }

    mux := http.NewServeMux()
    api := http.NewServeMux()
    mux.Handle("/api/v1/", http.StripPrefix("/api/v1", api))

    // ── Runtime Surface: cs-cloud 自身处理 ──
    api.HandleFunc("GET /runtime/health", s.handleHealth)
    api.HandleFunc("GET /runtime/target/context", s.handleTargetContext)
    api.HandleFunc("GET /runtime/files", s.handleFileList)
    api.HandleFunc("GET /runtime/files/content", s.handleFileContent)
    api.HandleFunc("GET /runtime/find/files", s.handleFindFiles)
    api.HandleFunc("GET /runtime/find/text", s.handleFindText)
    api.HandleFunc("GET /runtime/vcs", s.handleVCS)
    api.HandleFunc("POST /runtime/terminals", s.handleTerminalCreate)
    api.HandleFunc("POST /runtime/terminals/{id}/input", s.handleTerminalInput)
    api.HandleFunc("PATCH /runtime/terminals/{id}/size", s.handleTerminalResize)
    api.HandleFunc("DELETE /runtime/terminals/{id}", s.handleTerminalRemove)

    // ── Agent Surface: 路由到 agent runtime ──
    agentMw := s.agentRoutingMiddleware

    // Agent runtime 列表（无需 X-Backend）
    api.HandleFunc("GET /agents", s.handleAgentsList)

    // Agent runtime 操作（X-Backend 可选，省略则用默认 runtime）
    api.Handle("GET /agents/health", agentMw(http.HandlerFunc(s.handleAgentHealth)))
    api.Handle("GET /agents/models", agentMw(http.HandlerFunc(s.handleAgentModels)))
    api.Handle("GET /agents/models/config", agentMw(http.HandlerFunc(s.handleAgentModelConfigGet)))
    api.Handle("PATCH /agents/models/config", agentMw(http.HandlerFunc(s.handleAgentModelConfigPatch)))
    api.Handle("GET /agents/session-modes", agentMw(http.HandlerFunc(s.handleAgentSessionModes)))
    api.Handle("GET /agents/config", agentMw(http.HandlerFunc(s.handleAgentConfig)))

    // MCP & LSP（X-Backend 可选）
    api.Handle("GET /agents/mcp/status", agentMw(http.HandlerFunc(s.handleMCPStatus)))
    api.Handle("POST /agents/mcp/connections", agentMw(http.HandlerFunc(s.handleMCPConnect)))
    api.Handle("DELETE /agents/mcp/connections/{server_id}", agentMw(http.HandlerFunc(s.handleMCPDisconnect)))
    api.Handle("GET /agents/lsp/status", agentMw(http.HandlerFunc(s.handleLSPStatus)))

    // 对话流（需 X-Backend）
    api.Handle("POST /conversations", agentMw(http.HandlerFunc(s.handleConversationCreate)))
    api.Handle("GET /conversations", agentMw(http.HandlerFunc(s.handleConversationList)))
    api.Handle("GET /conversations/{id}", agentMw(http.HandlerFunc(s.handleConversationGet)))
    api.Handle("PATCH /conversations/{id}", agentMw(http.HandlerFunc(s.handleConversationUpdate)))
    api.Handle("DELETE /conversations/{id}", agentMw(http.HandlerFunc(s.handleConversationDelete)))
    api.Handle("POST /conversations/{id}/abort", agentMw(http.HandlerFunc(s.handleConversationAbort)))
    api.Handle("POST /conversations/{id}/prompt", agentMw(http.HandlerFunc(s.handleConversationPrompt)))
    api.Handle("POST /conversations/{id}/command", agentMw(http.HandlerFunc(s.handleConversationCommand)))
    api.Handle("POST /conversations/{id}/shell", agentMw(http.HandlerFunc(s.handleConversationShell)))
    api.Handle("GET /conversations/{id}/messages", agentMw(http.HandlerFunc(s.handleConversationMessages)))
    api.Handle("GET /conversations/{id}/diff", agentMw(http.HandlerFunc(s.handleConversationDiff)))
    api.Handle("GET /conversations/{id}/todo", agentMw(http.HandlerFunc(s.handleConversationTodo)))
    api.Handle("POST /conversations/{id}/revert", agentMw(http.HandlerFunc(s.handleConversationRevert)))
    api.Handle("POST /conversations/{id}/unrevert", agentMw(http.HandlerFunc(s.handleConversationUnrevert)))

    // 交互（需 X-Backend）
    api.Handle("GET /interactions/permissions", agentMw(http.HandlerFunc(s.handlePermissionsList)))
    api.Handle("POST /interactions/permissions/{id}/respond", agentMw(http.HandlerFunc(s.handlePermissionRespond)))
    api.Handle("GET /interactions/questions", agentMw(http.HandlerFunc(s.handleQuestionsList)))
    api.Handle("POST /interactions/questions/{id}/reply", agentMw(http.HandlerFunc(s.handleQuestionReply)))
    api.Handle("POST /interactions/questions/{id}/reject", agentMw(http.HandlerFunc(s.handleQuestionReject)))

    // 事件流（WebSocket，X-Backend 可选过滤）
    api.Handle("GET /events/stream", agentMw(http.HandlerFunc(s.handleEventStream)))

    s.http = &http.Server{
        Handler:           mux,
        ReadHeaderTimeout: 5 * time.Second,
    }
    return s
}
```

---

## Handler 内部路由逻辑

### 对话创建

```go
func (s *Server) handleConversationCreate(w http.ResponseWriter, r *http.Request) {
    backend, _ := r.Context().Value(ctxKeyBackend).(string)

    var body struct {
        WorkingDir  string `json:"working_dir"`
        ModelID     string `json:"model_id,omitempty"`
        SessionMode string `json:"session_mode,omitempty"`
    }
    json.NewDecoder(r.Body).Decode(&body)

    convID := uuid.NewString()
    cfg := agent.AgentConfig{
        ID:          convID,
        Backend:     backend,
        WorkingDir:  body.WorkingDir,
        ModelID:     body.ModelID,
        SessionMode: body.SessionMode,
    }

    if err := s.mgr.CreateAgent(r.Context(), convID, cfg); err != nil {
        writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
        return
    }

    ag, _ := s.mgr.GetAgent(convID)
    writeJSON(w, http.StatusCreated, envelope{OK: true, Data: map[string]any{
        "id":      convID,
        "backend": backend,
        "state":   ag.State().String(),
    }})
}
```

### 对话操作（通过 convID 路由）

```go
func (s *Server) handleConversationPrompt(w http.ResponseWriter, r *http.Request) {
    convID := r.PathValue("id")
    backend, _ := r.Context().Value(ctxKeyBackend).(string)

    ag, ok := s.mgr.GetAgent(convID)
    if !ok {
        writeErr(w, http.StatusNotFound, "NOT_FOUND", "conversation not found")
        return
    }

    // 校验：X-Backend 与 agent 实际 backend 是否一致
    if backend != "" && ag.Backend() != backend {
        writeErr(w, http.StatusConflict, "CONFLICT", fmt.Sprintf(
            "backend mismatch: agent is %s, request has %s", ag.Backend(), backend))
        return
    }

    var body struct {
        Content string   `json:"content"`
        Files   []string `json:"files,omitempty"`
    }
    json.NewDecoder(r.Body).Decode(&body)

    if err := ag.SendMessage(r.Context(), agent.PromptMessage{
        Content: body.Content,
        Files:   body.Files,
    }); err != nil {
        writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
        return
    }

    writeOK(w, nil)
}
```

### 权限列表（按 X-Backend 过滤）

```go
func (s *Server) handlePermissionsList(w http.ResponseWriter, r *http.Request) {
    backend, _ := r.Context().Value(ctxKeyBackend).(string)

    var all []agent.PermissionInfo
    for _, ag := range s.mgr.ListAgents() {
        if ag.Backend() != backend {
            continue
        }
        all = append(all, ag.PendingPermissions()...)
    }

    writeOK(w, map[string]any{"permissions": all})
}
```

---

## 内部架构：两层分发

```
                     ┌──────────────────────┐
                     │   HTTP Request        │
                     └──────────┬───────────┘
                                │
                    ┌───────────▼───────────┐
                    │   AuthMiddleware       │
                    └───────────┬───────────┘
                                │
              ┌─────────────────┼──────────────────┐
              │ 路径前缀判断     │                   │
              ▼                 ▼                   ▼
     /runtime/*         /agents/*           /conversations/*
     /interactions/*
     /events/*
              │                 │                   │
              ▼                 ▼                   ▼
     ┌────────────┐   ┌────────────────┐   ┌─────────────────┐
     │ cs-cloud   │   │ AgentRouting   │   │ AgentRouting    │
     │ 自身逻辑    │   │ Middleware     │   │ Middleware      │
     │            │   │ (解析X-Backend)│   │ (解析X-Backend) │
     └─────┬──────┘   └───────┬────────┘   └───────┬─────────┘
           │                  │                     │
           ▼                  ▼                     ▼
     ┌──────────┐     ┌──────────────┐     ┌──────────────┐
     │ Runtime  │     │ Registry     │     │ AgentManager │
     │ Handler  │     │ (查询/健康)   │     │ (convID→Agent)│
     └──────────┘     └──────┬───────┘     └──────┬───────┘
                              │                     │
                              ▼                     ▼
                       ┌──────────────┐     ┌──────────────┐
                       │ Agent        │     │ Agent        │
                       │ (实例方法)    │     │ (实例方法)    │
                       └──────┬───────┘     └──────┬───────┘
                              │                     │
                              ▼                     ▼
                       ┌──────────────────────────────────┐
                       │        ACP / WS / Custom Driver   │
                       │   (stdin JSON-RPC / WebSocket)    │
                       └──────────────────────────────────┘
```

关键区别：

| 维度 | Runtime Handler | Agent Handler |
|---|---|---|
| 请求来源 | 上游消费者 | 上游消费者 |
| 处理方 | cs-cloud 进程内 | 外部 agent 子进程/WS 连接 |
| 路由依据 | URL 路径 | `X-Backend` + `convID` |
| 状态 | cs-cloud 自身状态 | AgentManager 持有 Agent 实例 |
| 错误 | 直接返回 | 经 Adapter 转换后返回 |
| 事件 | 无 | 通过 EventBus 广播 |

---

## AgentManager 接口补充

```go
// internal/runtime/manager.go

type AgentManager struct {
    mu      sync.RWMutex
    agents  map[string]agent.Agent     // convID → Agent
    drivers map[string]agent.Driver     // driver name → Driver
    store   *SessionStore
    bus     *EventBus
    reg     *agent.Registry             // 引用 Registry 用于查询
}

// ── 对话操作（路由到 Agent 实例）──

func (m *AgentManager) CreateAgent(ctx context.Context, convID string, cfg agent.AgentConfig) error
func (m *AgentManager) GetAgent(convID string) (agent.Agent, bool)
func (m *AgentManager) SendMessage(ctx context.Context, convID string, msg agent.PromptMessage) error
func (m *AgentManager) CancelPrompt(convID string)
func (m *AgentManager) ConfirmPermission(convID string, callID string, optionID string) error
func (m *AgentManager) SetModel(ctx context.Context, convID string, modelID string) (*agent.ModelInfo, error)
func (m *AgentManager) SetMode(ctx context.Context, convID string, mode string) error
func (m *AgentManager) KillAgent(convID string)
func (m *AgentManager) KillAll()

// ── 查询操作（按 backend 过滤）──

func (m *AgentManager) ListAgents() []agent.Agent
func (m *AgentManager) ListAgentsByBackend(backend string) []agent.Agent
func (m *AgentManager) PendingPermissionsByBackend(backend string) []agent.PermissionInfo
func (m *AgentManager) PendingQuestionsByBackend(backend string) []agent.QuestionInfo

// ── Driver 管理 ──

func (m *AgentManager) RegisterDriver(d agent.Driver)
func (m *AgentManager) ResolveDriver(backend string) (agent.Driver, error)
```

---

## 对 app-ai-native 的影响

device-client.ts 中的调用路径对应关系：

| 当前路径 | 新路径 | 类型 | 说明 |
|---|---|---|---|
| `/api/v1/runtime/health` | `/api/v1/runtime/health` | Runtime | — |
| `/api/v1/runtime/files` | `/api/v1/runtime/files` | Runtime | — |
| `/api/v1/runtime/files/content` | `/api/v1/runtime/files/content` | Runtime | — |
| `/mcp` | `/api/v1/agents/mcp/status` | Agent | X-Backend 可选 |
| `/lsp` | `/api/v1/agents/lsp/status` | Agent | X-Backend 可选 |
| `/session` | `/api/v1/conversations` | Agent | X-Backend 可选 |
| `/session/:id/prompt_async` | `/api/v1/conversations/:id/prompt` | Agent | X-Backend 可选 |
| `/session/:id/abort` | `/api/v1/conversations/:id/abort` | Agent | X-Backend 可选 |
| `/session/:id/message` | `/api/v1/conversations/:id/messages` | Agent | X-Backend 可选 |
| `/permission` | `/api/v1/interactions/permissions` | Agent | X-Backend 可选 |
| `/question` | `/api/v1/interactions/questions` | Agent | X-Backend 可选 |
| `/provider/capabilities` | `/api/v1/agents/models` | Agent | X-Backend 可选 |
| `/provider/config` | `/api/v1/agents/models/config` | Agent | 模型/provider 配置，X-Backend 可选 |
| `/agent` | `/api/v1/agents/session-modes` | Agent | 会话模式列表，X-Backend 可选 |
| — | `/api/v1/agents` | Agent | 可用 runtime 列表 |

`X-Backend` header 全局可选。省略时 cs-cloud 自动选择默认 runtime（唯一可用或配置的默认值）。web 层无需感知 agent runtime 概念即可正常工作。
