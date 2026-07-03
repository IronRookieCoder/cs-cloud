# ACP Agent Integration Design Proposal

## Goal

Make `cs-cloud` an ACP-compatible runtime that can spawn, manage, and communicate with any ACP-compliant AI agent CLI (Claude Code, Qwen, Goose, Codex, OpenCode, etc.), exposing a unified RESTful + WebSocket surface to upstream consumers.

This document references the AionUi TypeScript implementation as the canonical ACP integration model, and maps each component to a Go implementation in `cs-cloud`.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                      Upstream Consumer                        │
│               (app-ai-native / cloud shell / SDK)              │
└──────────────┬──────────────────────────────┬────────────────┘
               │ REST (api/v1)                 │ WS (events)
┌──────────────▼──────────────────────────────▼────────────────┐
│                    cs-cloud LocalServer                       │
│  ┌────────────────────────────────────────────────────────┐   │
│  │               Runtime / Control Surface                │   │
│  │  health · target.context · models · file · find        │   │
│  │  mcp · lsp · vcs · terminal · instance.dispose        │   │
│  └────────────────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────────────────┐   │
│  │            Conversation Flow Surface                   │   │
│  │  conversation.* · interaction.* · event.stream           │   │
│  └────────────────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────────────────┐   │
│  │               Agent Manager Layer                      │   │
│  │     Agent interface · registry · factory · lifecycle   │   │
│  └────────────────────────────────────────────────────────┘   │
│  ┌──────────────────────┐  ┌─────────────────────────────┐   │
│  │   ACP Driver         │  │   Custom Driver              │   │
│  │  (stdin/stdout pipe) │  │  (HTTP / WS / gRPC / ...)   │   │
│  └──────────┬───────────┘  └──────────┬──────────────────┘   │
└─────────────┼─────────────────────────┼──────────────────────┘
              │                         │
   ┌──────────┼──────────┐     ┌────────┼─────────┐
   ▼          ▼          ▼     ▼                  ▼
┌──────┐ ┌──────┐ ┌──────┐ ┌──────────┐ ┌──────────┐
│Claude│ │ Qwen │ │Goose │ │ Internal │ │ 3rd-Party│
│(npx) │ │(CLI) │ │(CLI) │ │  Agent   │ │  Agent   │
└──────┘ └──────┘ └──────┘ └──────────┘ └──────────┘
```

### Agent Driver Model

cs-cloud uses a **driver-based architecture** to support horizontal extension of agent types. The `Agent` interface defines the unified contract; each driver provides a concrete implementation.

```
Agent (interface)
  ├── ACPDriver       — ACP JSON-RPC 2.0 over stdio (primary)
  ├── HTTPDriver      — REST/HTTP-based agents
  ├── WSDriver        — WebSocket-based agents (remote, openclaw-gateway)
  └── [pluggable]     — any future driver via Go plugin or compiled-in
```

**ACP is the first-class citizen** — all built-in CLI agents (Claude, Qwen, Goose, etc.) use ACP. Custom drivers exist for agents that don't speak ACP natively.

---

## AionUi Reference Mapping

| AionUi Module | AionUi File | cs-cloud Package | Status |
|---|---|---|---|
| — | — | `internal/agent/agent.go` | new (Agent interface) |
| — | — | `internal/agent/driver.go` | new (Driver interface) |
| — | — | `internal/agent/driver_custom.go` | new (CustomDriver) |
| ACP Types | `common/types/acpTypes.ts` | `internal/acp/types.go` | stub → implement |
| ACP Backend Registry | `acpTypes.ts` `ACP_BACKENDS_ALL` | `internal/acp/backend.go` | stub → implement |
| ACP Connection | `agent/acp/AcpConnection.ts` | `internal/acp/connection.go` | stub → implement |
| ACP Connector (spawn) | `agent/acp/acpConnectors.ts` | `internal/acp/connector.go` | new |
| ACP Adapter (msg convert) | `agent/acp/AcpAdapter.ts` | `internal/acp/adapter.go` | stub → implement |
| ACP Detector | `agent/acp/AcpDetector.ts` | `internal/acp/detector.go` | new |
| ACP Agent (lifecycle) | `agent/acp/index.ts` `AcpAgent` | `internal/acp/agent.go` | new (implements agent.Agent) |
| ACP Driver | — | `internal/acp/driver.go` | new (implements agent.Driver) |
| Permission Store | `agent/acp/ApprovalStore.ts` | `internal/acp/approval.go` | new |
| Model Info | `agent/acp/modelInfo.ts` | `internal/acp/model.go` | new |
| MCP Session Config | `agent/acp/mcpSessionConfig.ts` | `internal/acp/mcp.go` | new |
| Session Mode Constants | `agent/acp/constants.ts` | `internal/acp/constants.go` | new |
| Agent Manager | `task/AcpAgentManager.ts` | `internal/runtime/manager.go` | stub → redesign (multi-driver) |
| EventBus | `runtime/eventbus.go` | `internal/runtime/eventbus.go` | stub → implement |
| Session Store | `runtime/session_store.go` | `internal/runtime/session_store.go` | stub → implement |
| IPC Bridge | `bridge/acpConversationBridge.ts` | `internal/localserver/handlers/` | new |

---

## Component Design

### 1. ACP Protocol Types (`internal/acp/types.go`)

**Reference**: `acpTypes.ts` lines 582-972

```go
package acp

const JSONRPCVersion = "2.0"

type Request struct {
    JSONRPC string         `json:"jsonrpc"`
    ID      int64          `json:"id"`
    Method  string         `json:"method"`
    Params  map[string]any `json:"params,omitempty"`
}

type Response struct {
    JSONRPC string `json:"jsonrpc"`
    ID      int64  `json:"id"`
    Result  any    `json:"result,omitempty"`
    Error   *struct {
        Code    int64  `json:"code"`
        Message string `json:"message"`
    } `json:"error,omitempty"`
}

type Notification struct {
    JSONRPC string         `json:"jsonrpc"`
    Method  string         `json:"method"`
    Params  map[string]any `json:"params,omitempty"`
}

// ACP method constants
const (
    MethodInitialize        = "initialize"
    MethodAuthenticate      = "authenticate"
    MethodSessionNew        = "session/new"
    MethodSessionLoad       = "session/load"
    MethodSessionPrompt     = "session/prompt"
    MethodSessionCancel     = "session/cancel"
    MethodSessionSetMode    = "session/set_mode"
    MethodSessionSetModel   = "session/set_model"
    MethodSetConfigOption   = "session/set_config_option"
    MethodSessionUpdate     = "session/update"
    MethodRequestPermission = "session/request_permission"
    MethodReadTextFile      = "fs/read_text_file"
    MethodWriteTextFile     = "fs/write_text_file"
)

// Session update types
const (
    UpdateAgentMessageChunk = "agent_message_chunk"
    UpdateAgentThoughtChunk = "agent_thought_chunk"
    UpdateToolCall          = "tool_call"
    UpdateToolCallUpdate    = "tool_call_update"
    UpdatePlan              = "plan"
    UpdateAvailableCommands = "available_commands_update"
    UpdateConfigOption      = "config_option_update"
    UpdateUsage             = "usage_update"
    UpdateUserMessageChunk  = "user_message_chunk"
)
```

**Direct reference**: `acpTypes.ts:909-915` (`ACP_METHODS`), `acpTypes.ts:582-605` (JSON-RPC types)

---

### 2. Backend Registry (`internal/acp/backend.go`)

**Reference**: `acpTypes.ts:323-504` (`ACP_BACKENDS_ALL`)

```go
package acp

type BackendConfig struct {
    ID            string
    Name          string
    CLICommand    string   // e.g. "claude", "qwen", "goose"
    DefaultCLIPath string  // e.g. "npx @zed-industries/claude-agent-acp@0.21.0"
    AuthRequired  bool
    Enabled       bool
    SupportsStream bool
    ACPArgs       []string // e.g. ["--experimental-acp"], ["--acp"], ["acp"]
    YOLOMode      string   // e.g. "bypassPermissions", "yolo"
    Env           map[string]string
}

var BuiltInBackends = map[string]BackendConfig{
    "claude": {
        ID: "claude", Name: "Claude Code",
        CLICommand: "claude",
        DefaultCLIPath: "npx @zed-industries/claude-agent-acp@0.21.0",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{"--experimental-acp"},
        YOLOMode: "bypassPermissions",
    },
    "qwen": {
        ID: "qwen", Name: "Qwen Code",
        CLICommand: "qwen",
        DefaultCLIPath: "npx @qwen-code/qwen-code",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{"--acp"},
        YOLOMode: "yolo",
    },
    "codex": {
        ID: "codex", Name: "Codex",
        CLICommand: "codex",
        DefaultCLIPath: "npx @zed-industries/codex-acp@0.9.5",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{},
    },
    "goose": {
        ID: "goose", Name: "Goose",
        CLICommand: "goose",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"acp"}, // subcommand, not flag
    },
    "auggie": {
        ID: "auggie", Name: "Augment Code",
        CLICommand: "auggie",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"--acp"},
    },
    "opencode": {
        ID: "opencode", Name: "OpenCode",
        CLICommand: "opencode",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"acp"},
    },
    "copilot": {
        ID: "copilot", Name: "GitHub Copilot",
        CLICommand: "copilot",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"--acp", "--stdio"},
    },
    "droid": {
        ID: "droid", Name: "Factory Droid",
        CLICommand: "droid",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"exec", "--output-format", "acp"},
    },
    "kimi": {
        ID: "kimi", Name: "Kimi CLI",
        CLICommand: "kimi",
        AuthRequired: false, Enabled: true,
        ACPArgs: []string{"acp"},
    },
    "cursor": {
        ID: "cursor", Name: "Cursor Agent",
        CLICommand: "agent",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{"acp"},
    },
    "kiro": {
        ID: "kiro", Name: "Kiro",
        CLICommand: "kiro-cli",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{"acp"},
    },
    "codebuddy": {
        ID: "codebuddy", Name: "CodeBuddy",
        CLICommand: "codebuddy",
        DefaultCLIPath: "npx @tencent-ai/codebuddy-code",
        AuthRequired: true, Enabled: true,
        ACPArgs: []string{"--acp"},
        YOLOMode: "bypassPermissions",
    },
    // remote + custom: no CLI, connected via WebSocket or user config
    "remote": {ID: "remote", Name: "Remote Agent", Enabled: true, SupportsStream: true},
    "custom": {ID: "custom", Name: "Custom Agent", Enabled: true},
}
```

**Direct reference**: `acpTypes.ts:323-504` — all backend configs with their `acpArgs`, `cliCommand`, `authRequired` fields

---

### 3. Agent Detector (`internal/acp/detector.go`)

**Reference**: `AcpDetector.ts` (full file, 250 lines)

```go
package acp

type DetectedAgent struct {
    Backend   string
    Name      string
    CLIPath   string   // resolved absolute path
    ACPArgs   []string
    Available bool
}

type Detector struct {
    agents []DetectedAgent
    done   bool
}

func NewDetector() *Detector

// Scan system PATH for installed ACP CLIs
func (d *Detector) Initialize() error

// Return detected agents
func (d *Detector) Agents() []DetectedAgent

// Refresh custom agents from config
func (d *Detector) RefreshCustomAgents(config CustomAgentConfig) error
```

**Key logic to port**:
- `which`/`where` CLI detection (`AcpDetector.ts:137-171`)
- Windows PowerShell fallback for `.ps1` shims (`AcpDetector.ts:155-165`)
- Enhanced PATH from user shell (`getEnhancedEnv`)
- Custom agents from config (`AcpDetector.ts:87-119`)
- Extension-contributed adapters (`AcpDetector.ts:40-81`)

---

### 4. ACP Connection (`internal/acp/connection.go`)

**Reference**: `AcpConnection.ts` (1164 lines) — the most complex module

```go
package acp

type Connection struct {
    cmd          *exec.Cmd
    stdin        io.WriteCloser
    stdout       *bufio.Scanner
    backend      string
    sessionID    string
    initialized bool

    pending      map[int64]*pendingRequest
    nextID       int64
    mu           sync.Mutex

    // Callbacks
    OnSessionUpdate    func(update map[string]any)
    OnPermissionRequest func(req PermissionRequest) PermissionResponse
    OnEndTurn          func()
    OnDisconnect       func(code int, signal string)
    OnFileOperation    func(op FileOperation)
}

type pendingRequest struct {
    ch     chan any
    method string
    // timeout handling
}

// Lifecycle
func (c *Connection) Connect(backend string, cliPath string, workDir string, acpArgs []string, env map[string]string) error
func (c *Connection) Disconnect() error

// Protocol
func (c *Connection) Initialize() (*InitializeResult, error)
func (c *Connection) Authenticate(methodID string) error
func (c *Connection) NewSession(cwd string, opts SessionOptions) (*SessionResult, error)
func (c *Connection) LoadSession(sessionID string, cwd string) (*SessionResult, error)
func (c *Connection) SendPrompt(prompt string) error
func (c *Connection) CancelPrompt()
func (c *Connection) SetSessionMode(mode string) error
func (c *Connection) SetModel(modelID string) error
func (c *Connection) SetConfigOption(configID string, value string) error
func (c *Connection) GetConfigOptions() []ConfigOption
func (c *Connection) GetModels() *SessionModels

// Internal
func (c *Connection) sendMessage(msg any) error
func (c *Connection) handleMessage(msg map[string]any)
func (c *Connection) handleIncomingRequest(msg map[string]any) (any, error)
func (c *Connection) handlePermissionRequest(params map[string]any) (any, error)
func (c *Connection) handleReadFile(params map[string]any) (any, error)
func (c *Connection) handleWriteFile(params map[string]any) (any, error)
```

**Key logic to port**:
- JSON-RPC 2.0 over stdin/stdout, line-delimited (`AcpConnection.ts:392-417`)
- Pending request map with timeout + keepalive (`AcpConnection.ts:474-654`)
- Permission request pause/resume timeout (`AcpConnection.ts:771-801`)
- Session resume via `_meta.claudeCode.options.resume` (`AcpConnection.ts:858-879`)
- CWD normalization per backend (`AcpConnection.ts:951-977`)
- Process exit handling with startup vs runtime distinction (`AcpConnection.ts:340-375`)
- Auto-reconnect on unexpected disconnect (`AcpAgent.ts:607-619`)

---

### 5. Connector / Spawner (`internal/acp/connector.go`)

**Reference**: `acpConnectors.ts` (653 lines)

```go
package acp

type SpawnResult struct {
    Cmd       *exec.Cmd
    Detached  bool
}

// Environment preparation
func PrepareCleanEnv() []string
func EnsureMinNodeVersion(env []string, minMajor, minMinor int, label string) error

// Per-backend connectors
func ConnectClaude(workDir string) (SpawnResult, error)
func ConnectCodex(workDir string) (SpawnResult, error)
func ConnectCodebuddy(workDir string) (SpawnResult, error)
func ConnectGeneric(backend string, cliPath string, workDir string, acpArgs []string, env map[string]string) (SpawnResult, error)

// NPX retry strategy
func ConnectNpxBackend(cfg NpxConfig) (SpawnResult, error)
```

**Key logic to port**:
- Phase 1/2 npx retry: `--prefer-offline` then fresh (`acpConnectors.ts:493-531`)
- Clean env: strip `NODE_OPTIONS`, `npm_*`, `CLAUDECODE` (`acpConnectors.ts:115-133`)
- Node version check + PATH auto-correction (`acpConnectors.ts:140-199`)
- Codex platform binary cache resolution (`acpConnectors.ts:377-425`)
- Windows `chcp 65001` for UTF-8 (`acpConnectors.ts:244`)
- CodeBuddy `detached: true` for `/dev/tty` (`acpConnectors.ts:651`)

---

### 6. Agent Lifecycle (`internal/acp/agent.go`)

**Reference**: `AcpAgent` in `index.ts` (1306+ lines)

```go
package acp

type Agent struct {
    id        string
    backend   string
    conn      *Connection
    adapter   *Adapter
    approval  *ApprovalStore
    modelInfo *ModelInfo

    // State
    yoloMode           bool
    userModelOverride  string
    pendingPermissions map[string]chan PermissionResponse
}

type AgentConfig struct {
    ID         string
    Backend    string
    CLIPath    string
    WorkingDir string
    CustomArgs []string
    CustomEnv  map[string]string
    YOLOMode   bool
    ModelID    string
    SessionMode string
}

func NewAgent(cfg AgentConfig) *Agent

// Lifecycle
func (a *Agent) Start() error
func (a *Agent) Kill() error
func (a *Agent) SendMessage(content string, files []string) error
func (a *Agent) CancelPrompt()
func (a *Agent) ConfirmPermission(callID string, optionID string) error

// Model
func (a *Agent) GetModelInfo() *ModelInfo
func (a *Agent) SetModel(modelID string) (*ModelInfo, error)
func (a *Agent) SetSessionMode(mode string) error
func (a *Agent) SetConfigOption(configID string, value string) error
```

**Key logic to port**:
- Start sequence: connect → authenticate → session → mode → model (`index.ts:273-404`)
- Auto-reconnect on send (`index.ts:607-619`)
- @-file reference processing (`index.ts:768-863`)
- Model switch notice injection for Claude (`index.ts:681-691`)
- ApprovalStore auto-approve (`index.ts:1061-1137`)

---

### 7. Message Adapter (`internal/acp/adapter.go`)

**Reference**: `AcpAdapter.ts` (295 lines)

```go
package acp

type Adapter struct {
    conversationID string
    backend        string
    activeToolCalls map[string]*ToolCallState
    currentMsgID   string
}

func NewAdapter(convID string, backend string) *Adapter

// Convert ACP session/update to unified event
func (a *Adapter) ConvertSessionUpdate(update map[string]any) []Event

func (a *Adapter) ResetMessageTracking()
```

**Key logic to port**:
- `agent_message_chunk` → text event with shared `msg_id` for accumulation (`AcpAdapter.ts:146-167`)
- `tool_call` / `tool_call_update` → tool call events with merge-by-toolCallId (`AcpAdapter.ts:194-265`)
- `plan` → plan event (`AcpAdapter.ts:270-292`)
- `agent_thought_chunk` → tips event (`AcpAdapter.ts:172-192`)

---

### 8. Approval Store (`internal/acp/approval.go`)

**Reference**: `ApprovalStore.ts`

```go
package acp

type ApprovalStore struct {
    mu    sync.RWMutex
    store map[string]string // key -> "allow_always"
}

func NewApprovalStore() *ApprovalStore
func (s *ApprovalStore) IsApproved(key string) bool
func (s *ApprovalStore) Put(key string, decision string)
func (s *ApprovalStore) Clear()
```

**Direct reference**: Used in `AcpAgent.ts:166` and `index.ts:922-943` for auto-approving repeated permissions.

---

### 9. MCP Session Config (`internal/acp/mcp.go`)

**Reference**: `mcpSessionConfig.ts` (125 lines)

```go
package acp

type MCPServerStdio struct {
    Type    string            `json:"type"`
    Name    string            `json:"name"`
    Command string            `json:"command"`
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

type MCPServerHTTP struct {
    Type    string            `json:"type"`
    Name    string            `json:"name"`
    URL     string            `json:"url"`
    Headers map[string]string `json:"headers,omitempty"`
}

type MCPServer = MCPServerStdio | MCPServerHTTP // via interface

type MCPCapabilities struct {
    Stdio bool
    HTTP  bool
    SSE   bool
}

func ParseMCPCapabilities(initResult map[string]any) MCPCapabilities
func BuildBuiltinMCPServers(servers []MCPServerConfig, caps MCPCapabilities) []MCPServer
```

---

### 10. Model Info (`internal/acp/model.go`)

**Reference**: `modelInfo.ts` (55 lines)

```go
package acp

type ModelInfo struct {
    CurrentModelID    string
    CurrentModelLabel string
    AvailableModels   []ModelEntry
    CanSwitch         bool
    Source            string // "configOption" | "models"
    ConfigOptionID    string
}

type ModelEntry struct {
    ID    string
    Label string
}

func BuildModelInfo(configOptions []ConfigOption, models *SessionModels) *ModelInfo
```

**Direct reference**: `modelInfo.ts:3-37` — dual-source model info resolution (stable configOptions vs unstable models API).

---

### 11. Session Mode Constants (`internal/acp/constants.go`)

**Reference**: `constants.ts` (38 lines)

```go
package acp

const (
    ClaudeYOLOMode    = "bypassPermissions"
    QwenYOLOMode      = "yolo"
    IFlowYOLOMode     = "yolo"
    CodebuddyYOLOMode = "bypassPermissions"
    GooseYOLOEnvVar   = "GOOSE_MODE"
    GooseYOLOEnvValue = "auto"
)
```

---

### 12. Agent Interface (`internal/agent/agent.go`)

The core abstraction that all agent drivers implement. This decouples the manager from any specific communication protocol.

```go
package agent

type Agent interface {
    // Identity
    ID() string
    Backend() string
    State() AgentState

    // Lifecycle
    Start(ctx context.Context) error
    Kill() error

    // Conversation
    SendMessage(ctx context.Context, msg PromptMessage) error
    CancelPrompt()

    // Permission (human-in-the-loop)
    ConfirmPermission(callID string, optionID string) error
    PendingPermissions() []PermissionInfo

    // Model & Config
    GetModelInfo() *ModelInfo
    SetModel(ctx context.Context, modelID string) (*ModelInfo, error)
    SetSessionMode(ctx context.Context, mode string) error
    SetConfigOption(ctx context.Context, configID string, value string) error
    GetConfigOptions() []ConfigOption

    // Session resume
    SessionID() string
    SetEventEmitter(emitter func(Event))
}

type AgentState int

const (
    StateIdle AgentState = iota
    StateConnecting
    StateConnected
    StateAuthenticated
    StateSessionActive
    StateDisconnected
    StateError
)

type PromptMessage struct {
    Content string   `json:"content"`
    Files   []string `json:"files,omitempty"`
}

type PermissionInfo struct {
    CallID    string `json:"call_id"`
    Title     string `json:"title"`
    Kind      string `json:"kind"`
    Options   []PermissionOption `json:"options"`
}

type PermissionOption struct {
    OptionID string `json:"option_id"`
    Name     string `json:"name"`
    Kind     string `json:"kind"` // allow_once, allow_always, reject_once, reject_always
}

type ModelInfo struct {
    CurrentModelID    string       `json:"current_model_id"`
    CurrentModelLabel string       `json:"current_model_label"`
    AvailableModels   []ModelEntry `json:"available_models"`
    CanSwitch         bool         `json:"can_switch"`
    Source            string       `json:"source"`
    ConfigOptionID    string       `json:"config_option_id,omitempty"`
}

type ModelEntry struct {
    ID    string `json:"id"`
    Label string `json:"label"`
}

type ConfigOption struct {
    ID           string   `json:"id"`
    Name         string   `json:"name"`
    Category     string   `json:"category"`
    Type         string   `json:"type"` // select, boolean, string
    CurrentValue string   `json:"current_value,omitempty"`
    Options      []string `json:"options,omitempty"`
}
```

---

### 13. Agent Driver (`internal/agent/driver.go`)

The driver is the factory + lifecycle hook for a specific communication protocol. Each driver knows how to create, detect, and configure agents of its type.

```go
package agent

type Driver interface {
    // Identity
    Name() string // "acp", "http", "ws", "custom"

    // Detection
    Detect() ([]DetectedAgent, error)

    // Factory
    CreateAgent(cfg AgentConfig) (Agent, error)

    // Health check
    HealthCheck(ctx context.Context, backend string) (*HealthResult, error)
}

type DetectedAgent struct {
    Backend   string   `json:"backend"`
    Name      string   `json:"name"`
    Driver    string   `json:"driver"` // which driver detected this
    CLIPath   string   `json:"cli_path,omitempty"`
    ACPArgs   []string `json:"acp_args,omitempty"`
    Available bool     `json:"available"`
}

type HealthResult struct {
    Available bool   `json:"available"`
    LatencyMs int64  `json:"latency_ms,omitempty"`
    Error     string `json:"error,omitempty"`
}

type AgentConfig struct {
    ID          string            `json:"id"`
    Backend     string            `json:"backend"`
    WorkingDir  string            `json:"working_dir"`
    CLIPath     string            `json:"cli_path,omitempty"`
    CustomArgs  []string          `json:"custom_args,omitempty"`
    CustomEnv   map[string]string `json:"custom_env,omitempty"`
    YOLOMode    bool              `json:"yolo_mode,omitempty"`
    ModelID     string            `json:"model_id,omitempty"`
    SessionMode string            `json:"session_mode,omitempty"`
    Extra       map[string]any    `json:"extra,omitempty"`
}
```

---

### 14. ACP Driver Implementation (`internal/acp/driver.go`)

The primary driver — implements `agent.Driver` for all ACP-compliant CLI agents.

```go
package acp

type ACPDriver struct {
    backends map[string]BackendConfig
}

func NewACPDriver() *ACPDriver

func (d *ACPDriver) Name() string { return "acp" }

func (d *ACPDriver) Detect() ([]agent.DetectedAgent, error)
func (d *ACPDriver) CreateAgent(cfg agent.AgentConfig) (agent.Agent, error)
func (d *ACPDriver) HealthCheck(ctx context.Context, backend string) (*agent.HealthResult, error)
```

The `ACPDriver.CreateAgent` returns an `*ACPAgent` which implements the `agent.Agent` interface by delegating to `AcpConnection`.

---

### 15. Custom Driver Skeleton (`internal/agent/driver_custom.go`)

Built-in skeleton for user-defined drivers. Users can implement `agent.Driver` and register it at startup.

```go
package agent

type CustomDriver struct {
    name    string
    factory func(cfg AgentConfig) (Agent, error)
    detect  func() ([]DetectedAgent, error)
    health  func(ctx context.Context, backend string) (*HealthResult, error)
}

func NewCustomDriver(name string, opts CustomDriverOpts) *CustomDriver

func (d *CustomDriver) Name() string { return d.name }
func (d *CustomDriver) Detect() ([]DetectedAgent, error)
func (d *CustomDriver) CreateAgent(cfg AgentConfig) (Agent, error)
func (d *CustomDriver) HealthCheck(ctx context.Context, backend string) (*HealthResult, error)
```

**Example**: A WebSocket-based remote agent driver

```go
type WSDriver struct{}

func (d *WSDriver) Name() string { return "ws" }

func (d *WSDriver) CreateAgent(cfg agent.AgentConfig) (agent.Agent, error) {
    wsURL, _ := cfg.Extra["url"].(string)
    return NewWSAgent(cfg, wsURL), nil
}
```

---

### 16. Agent Manager (`internal/runtime/manager.go`)

**Reference**: `AcpAgentManager.ts` (696+ lines), generalized to multi-driver

```go
package runtime

type AgentManager struct {
    mu       sync.RWMutex
    agents   map[string]agent.Agent // conversationID -> Agent
    drivers  map[string]agent.Driver // driver name -> Driver
    eventBus *EventBus
    store    *SessionStore
}

func NewAgentManager(eventBus *EventBus, store *SessionStore) *AgentManager

// Driver management
func (m *AgentManager) RegisterDriver(driver agent.Driver)
func (m *AgentManager) DeregisterDriver(name string)
func (m *AgentManager) GetDriver(name string) (agent.Driver, bool)

// Agent lifecycle — routes to correct driver via cfg.Driver
func (m *AgentManager) CreateAgent(convID string, cfg agent.AgentConfig) error
func (m *AgentManager) GetAgent(convID string) (agent.Agent, bool)
func (m *AgentManager) SendMessage(ctx context.Context, convID string, msg agent.PromptMessage) error
func (m *AgentManager) CancelPrompt(convID string)
func (m *AgentManager) ConfirmPermission(convID string, callID string, optionID string) error
func (m *AgentManager) SetModel(ctx context.Context, convID string, modelID string) (*agent.ModelInfo, error)
func (m *AgentManager) SetMode(ctx context.Context, convID string, mode string) error
func (m *AgentManager) KillAgent(convID string)
func (m *AgentManager) KillAll()

// Detection — aggregates from all registered drivers
func (m *AgentManager) DetectAgents() ([]agent.DetectedAgent, error)
func (m *AgentManager) HealthCheck(ctx context.Context, backend string) (*agent.HealthResult, error)
```

**Key design**: `CreateAgent` reads `cfg.Driver` to select the correct `Driver`, then calls `driver.CreateAgent(cfg)`. The manager never knows about ACP vs HTTP vs WS — it only speaks `agent.Agent`.

**Initialization example**:

```go
mgr := runtime.NewAgentManager(eventBus, store)

// Register built-in ACP driver (primary)
mgr.RegisterDriver(acp.NewACPDriver())

// Register WebSocket remote agent driver
mgr.RegisterDriver(ws.NewWSDriver())

// Register user-provided custom driver
mgr.RegisterDriver(agent.NewCustomDriver("my-agent", opts))

// Detection aggregates from all drivers
agents, _ := mgr.DetectAgents()
// → [{Backend:"claude", Driver:"acp"}, {Backend:"remote", Driver:"ws"}, ...]
```

---

### 13. Event Bus (`internal/runtime/eventbus.go`)

**Reference**: AionUi uses IPC bridge `acpConversation.responseStream.emit()`; cs-cloud should use Go channels + WebSocket broadcast.

```go
package runtime

type EventBus struct {
    mu          sync.RWMutex
    subscribers map[chan Event]struct{}
}

type Event struct {
    Type           string `json:"type"`
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"msg_id"`
    Data           any    `json:"data"`
}

func NewEventBus() *EventBus
func (b *EventBus) Subscribe() chan Event
func (b *EventBus) Unsubscribe(ch chan Event)
func (b *EventBus) Emit(event Event)
```

---

### 14. Session Store (`internal/runtime/session_store.go`)

**Reference**: AionUi uses SQLite + better-sqlite3; cs-cloud should use a lightweight store (SQLite via CGO or bbolt).

```go
package runtime

type SessionRecord struct {
    ID              string
    AcpSessionID    string
    Backend         string
    WorkingDir      string
    ModelID         string
    CreatedAt       int64
    UpdatedAt       int64
}

type SessionStore struct {
    // implementation: bbolt or sqlite
}

func NewSessionStore(path string) (*SessionStore, error)
func (s *SessionStore) Save(record SessionRecord) error
func (s *SessionStore) Get(id string) (*SessionRecord, error)
func (s *SessionStore) List() ([]SessionRecord, error)
func (s *SessionStore) Delete(id string) error
```

---

## RESTful API Surface for Agent Operations

These extend the previously defined `runtime-control-api.md` with ACP conversation endpoints.

### Agent Routing Headers

All agent endpoints (conversations, interactions, agents/*) use HTTP headers to route requests to the correct agent driver:

| Header | Required | Description |
|---|---|---|
| `X-Backend` | no | Target backend ID, e.g. `claude`, `qwen`, `goose`. Omit to use default runtime |

When `X-Backend` is omitted, cs-cloud selects the default runtime:
- If only one runtime is available → use it
- If multiple → use the one configured as default in `agents.toml`
- If none → return 503 `UNAVAILABLE`

Driver is resolved internally by cs-cloud based on backend ID:

| Backend | Driver |
|---|---|
| Any key in `BuiltInBackends` | `acp` |
| `remote` | `ws` |
| Others | `custom` |

Routing logic:

1. `POST /conversations` — reads `X-Backend` to select driver and create agent instance
2. `/conversations/:id/*` — primarily routed via `conversationID → Agent` mapping stored in `AgentManager`; `X-Backend` used as validation hint
3. `/agents/*` — uses `X-Backend` to select target runtime for health/models/session-modes/config/mcp/lsp queries
4. `/interactions/*` — uses `X-Backend` to filter/query the correct agent's pending state
5. `WS /events/stream` — uses `X-Backend` to subscribe to events from the correct agent

Auto-inference rules when `X-Driver` is omitted:

| Backend | Inferred Driver |
|---|---|
| Any key in `BuiltInBackends` | `acp` |
| `remote` | `ws` |
| Others | `custom` |

### Conversation Flow

| Method | Path | ACP Method | Description |
|---|---|---|---|
| `POST` | `/conversations` | `session/new` | Create conversation |
| `GET` | `/conversations/:id` | — | Get conversation info |
| `GET` | `/conversations` | — | List conversations |
| `PATCH` | `/conversations/:id` | `session/set_mode` / `session/set_model` | Update conversation config |
| `DELETE` | `/conversations/:id` | — | Delete conversation |
| `POST` | `/conversations/:id/abort` | `session/cancel` | Abort current prompt |
| `POST` | `/conversations/:id/revert` | — | Revert conversation |
| `POST` | `/conversations/:id/unrevert` | — | Unrevert conversation |
| `GET` | `/conversations/:id/messages` | — | Get message history |
| `POST` | `/conversations/:id/prompt` | `session/prompt` | Send user prompt |
| `POST` | `/conversations/:id/command` | — | Execute slash command |
| `POST` | `/conversations/:id/shell` | — | Execute shell command |
| `GET` | `/conversations/:id/diff` | — | Get pending diffs |
| `GET` | `/conversations/:id/todo` | — | Get todo list |

### Interaction (Human-in-the-loop)

| Method | Path | Description |
|---|---|---|
| `GET` | `/interactions/permissions` | List pending permission requests |
| `POST` | `/interactions/permissions/:id/respond` | Respond to permission request |
| `GET` | `/interactions/questions` | List pending questions |
| `POST` | `/interactions/questions/:id/reply` | Reply to question |
| `POST` | `/interactions/questions/:id/reject` | Reject question |

### Event Stream

| Method | Path | Description |
|---|---|---|
| `WS` | `/events/stream` | WebSocket event stream (all conversation events) |

### Agent Detection & Health

| Method | Path | Description |
|---|---|---|
| `GET` | `/agents` | List all detected agent runtimes (aggregated from all drivers) |
| `GET` | `/agents/health` | Health check for agent runtime (X-Backend selects, default if omitted) |
| `GET` | `/agents/models` | Probe model info for agent runtime (X-Backend selects) |
| `GET` | `/agents/models/config` | Get model/provider configuration for agent runtime (X-Backend selects) |
| `PATCH` | `/agents/models/config` | Update model/provider configuration for agent runtime (X-Backend selects) |
| `GET` | `/agents/session-modes` | List session modes for agent runtime (X-Backend selects) |
| `GET` | `/agents/config` | Get/set agent runtime configuration (X-Backend selects) |
| `GET` | `/drivers` | List registered drivers |
| `POST` | `/drivers` | Register a custom driver (config-based) |

### Agent MCP & LSP

MCP and LSP are per-agent session resources, not global runtime state.

| Method | Path | Description |
|---|---|---|
| `GET` | `/agents/mcp/status` | Get MCP server status (X-Backend selects) |
| `POST` | `/agents/mcp/connections` | Connect MCP server (X-Backend selects) |
| `DELETE` | `/agents/mcp/connections/:server_id` | Disconnect MCP server (X-Backend selects) |
| `GET` | `/agents/lsp/status` | Get LSP server status (X-Backend selects) |

---

## Implementation Priority

### Phase 1: Core Abstraction + ACP Driver (bootstrap)

1. `agent/agent.go` — `Agent` interface + shared types (AgentState, PromptMessage, ModelInfo, etc.)
2. `agent/driver.go` — `Driver` interface + AgentConfig + DetectedAgent
3. `acp/types.go` — JSON-RPC 2.0 types + ACP method constants
4. `acp/connection.go` — Process spawn + JSON-RPC message loop
5. `acp/connector.go` — Clean env + per-backend spawn logic
6. `acp/backend.go` — Backend registry
7. `acp/detector.go` — CLI detection
8. `acp/driver.go` — `ACPDriver` implementing `agent.Driver`
9. `acp/agent.go` — `ACPAgent` implementing `agent.Agent`

### Phase 2: Manager + Conversation Lifecycle (standard)

10. `runtime/manager.go` — Multi-driver AgentManager
11. `acp/adapter.go` — Message format conversion
12. `acp/approval.go` — Permission auto-approve store
13. `acp/model.go` — Model info resolution
14. `acp/constants.go` — Session mode constants
15. `runtime/eventbus.go` — Event broadcast
16. `runtime/session_store.go` — Session persistence

### Phase 3: Custom Drivers + MCP + Advanced (lazy)

17. `agent/driver_custom.go` — CustomDriver skeleton
18. `acp/mcp.go` — MCP session config injection
19. `acp/utils.go` — File operations, workspace path resolution
20. Conversation REST handlers
21. Interaction REST handlers
22. WebSocket event stream

---

## Key Design Decisions

### 1. Go vs TypeScript Differences

| Concern | AionUi (TypeScript) | cs-cloud (Go) |
|---|---|---|
| Concurrency | Single-threaded + event loop | Goroutines per connection |
| IPC | Electron IPC bridge | REST + WebSocket |
| Process mgmt | `child_process.spawn` | `os/exec.Cmd` |
| JSON-RPC I/O | stdout line reader + buffer | `bufio.Scanner` on stdout pipe |
| Timeout | `setTimeout` + keepalive | `context.WithTimeout` + goroutine keepalive |
| State | Class instance fields | Struct + `sync.Mutex` |

### 2. Why Not Just Wrap AionUi

- cs-cloud runs as a headless local server, no Electron
- Go provides better process management, lower memory, no Node.js dependency
- REST + WS is a universal interface, not tied to Electron IPC
- cs-cloud must work in cloud-deployed scenarios (tunnel mode)

### 3. Protocol Compatibility

cs-cloud should be a **transparent ACP proxy** — it does not reinterpret ACP messages, only routes them. This means:
- Any future ACP method additions are automatically supported
- Backend-specific quirks (Claude's `_meta`, Codex's `session/load`) are handled at the connector layer, not the protocol layer
- The REST surface is a convenience mapping, not a replacement for direct ACP access

### 4. Session Resume Strategy

**Reference**: `AcpConnection.ts:847-891`

- Claude/CodeBuddy: `_meta.claudeCode.options.resume` in `session/new`
- Codex: `session/load` method
- Generic: `resumeSessionId` parameter in `session/new`
- cs-cloud should persist `acpSessionID` in `SessionStore` and pass it on reconnect

### 5. Driver Extensibility

The `agent.Driver` interface is the extension point. Adding support for a new communication protocol requires only:

1. Implement `agent.Driver` (3 methods: `Name`, `Detect`, `CreateAgent`)
2. Implement `agent.Agent` (the actual agent lifecycle)
3. Register via `AgentManager.RegisterDriver(driver)`

No changes to the manager, event bus, REST handlers, or upstream consumers.

**Built-in drivers**:
- `acp` — all ACP-compliant CLI agents (primary, covers 15+ backends)
- `ws` — WebSocket-based remote agents (openclaw-gateway pattern)

**User-provided drivers**:
- Compiled into the binary (Go import)
- Or loaded at runtime via `CustomDriver` with injected factory functions

**Driver selection flow** (resolved internally by `Registry.InferDriver(backend)`):
```
AgentConfig{Backend: "claude"}  → InferDriver("claude")  → "acp" → ACPDriver → ACPAgent
AgentConfig{Backend: "qwen"}    → InferDriver("qwen")    → "acp" → ACPDriver → ACPAgent
AgentConfig{Backend: "remote"}  → InferDriver("remote")  → "ws"  → WSDriver  → WSAgent
AgentConfig{Backend: "my-ai"}  → InferDriver("my-ai")   → "custom" → CustomDriver → MyAgent
```

### 6. ACP Driver vs Custom Driver Boundary

| Concern | ACP Driver | Custom Driver |
|---|---|---|
| Communication | stdin/stdout JSON-RPC 2.0 | Any (HTTP, WS, gRPC, in-process) |
| Agent discovery | `which`/`where` CLI detection | User config or service registry |
| Session mgmt | ACP `session/new` / `session/prompt` | Driver-defined |
| Permission | ACP `session/request_permission` | Driver-defined |
| Model switching | ACP `session/set_model` | Driver-defined |
| File ops | ACP `fs/read_text_file` / `fs/write_text_file` | Driver-defined |
| Event format | ACP `session/update` notifications | Driver-defined, converted to `agent.Event` |

The key constraint: **all drivers must emit `agent.Event`** through the `EventEmitter` callback. The event format is unified regardless of the underlying protocol. This ensures the REST/WS surface and UI layer remain driver-agnostic.
