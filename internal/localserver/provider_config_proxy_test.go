package localserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/runtime"
)

type providerConfigProxyBackendRequest struct {
	method string
	path   string
	body   string
}

type providerConfigProxyDriver struct {
	endpoint string
}

func (d *providerConfigProxyDriver) Name() string { return "provider-config-proxy" }

func (d *providerConfigProxyDriver) Detect(context.Context) ([]agent.DetectedAgent, error) {
	return nil, nil
}

func (d *providerConfigProxyDriver) CreateAgent(cfg agent.AgentConfig) (agent.Agent, error) {
	return &providerConfigProxyAgent{id: cfg.ID, endpoint: d.endpoint}, nil
}

func (d *providerConfigProxyDriver) HealthCheck(context.Context, string) (*agent.HealthResult, error) {
	return nil, nil
}

func (d *providerConfigProxyDriver) ProxyRoutes() []agent.ProxyRoute {
	return []agent.ProxyRoute{
		{Method: http.MethodGet, Prefix: "/provider/config", Rewrite: agent.RewriteTo("/provider/config")},
		{Method: http.MethodPatch, Prefix: "/provider/config", Rewrite: agent.RewriteTo("/provider/config")},
	}
}

func (d *providerConfigProxyDriver) HeaderMap() map[string]string { return map[string]string{} }

func (d *providerConfigProxyDriver) FetchCommands(string) ([]agent.SlashCommand, error) {
	return nil, nil
}

func (d *providerConfigProxyDriver) PrewarmPaths() []string { return nil }

func (d *providerConfigProxyDriver) Version() (string, error) { return "test", nil }

type providerConfigProxyAgent struct {
	id           string
	endpoint     string
	eventEmitter func(agent.Event)
}

func (a *providerConfigProxyAgent) ID() string { return a.id }

func (a *providerConfigProxyAgent) Backend() string { return "provider-config-proxy" }

func (a *providerConfigProxyAgent) Driver() string { return "http" }

func (a *providerConfigProxyAgent) State() agent.AgentState { return agent.StateConnected }

func (a *providerConfigProxyAgent) PID() int { return 0 }

func (a *providerConfigProxyAgent) Start(context.Context) error { return nil }

func (a *providerConfigProxyAgent) Kill() error { return nil }

func (a *providerConfigProxyAgent) SendMessage(context.Context, agent.PromptMessage) error {
	return nil
}

func (a *providerConfigProxyAgent) CancelPrompt(context.Context) error { return nil }

func (a *providerConfigProxyAgent) ConfirmPermission(context.Context, string, string) error {
	return nil
}

func (a *providerConfigProxyAgent) PendingPermissions() []agent.PermissionInfo { return nil }

func (a *providerConfigProxyAgent) GetModelInfo() *agent.ModelInfo { return nil }

func (a *providerConfigProxyAgent) SetModel(context.Context, string) (*agent.ModelInfo, error) {
	return nil, nil
}

func (a *providerConfigProxyAgent) SessionID() string { return "" }

func (a *providerConfigProxyAgent) SetEventEmitter(emitter func(agent.Event)) {
	a.eventEmitter = emitter
}

func (a *providerConfigProxyAgent) Endpoint() string { return a.endpoint }

func TestProviderConfigProxyForwardsRequestsToBackend(t *testing.T) {
	received := make(chan providerConfigProxyBackendRequest, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read backend body: %v", err)
		}
		received <- providerConfigProxyBackendRequest{
			method: r.Method,
			path:   r.URL.Path,
			body:   string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	srv := New(WithVersion("test"))
	srv.manager = runtime.NewAgentManager(srv.eventBus)
	srv.manager.RegisterDriver(&providerConfigProxyDriver{endpoint: backend.URL})
	if err := srv.manager.CreateAgent(context.Background(), "default", agent.AgentConfig{
		ID:         "default",
		Backend:    "provider-config-proxy",
		DriverName: "http",
	}); err != nil {
		t.Fatalf("CreateAgent() error: %v", err)
	}

	tests := []struct {
		name     string
		method   string
		body     string
		wantBody string
	}{
		{name: "get", method: http.MethodGet},
		{
			name:     "patch",
			method:   http.MethodPatch,
			body:     `{"provider":"openai","apiKey":"sk-test-redacted","baseUrl":"https://api.example.com/v1"}`,
			wantBody: `{"provider":"openai","apiKey":"sk-test-redacted","baseUrl":"https://api.example.com/v1"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/provider/config", strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()

			srv.http.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}

			got := <-received
			if got.method != tt.method {
				t.Fatalf("backend method = %s, want %s", got.method, tt.method)
			}
			if got.path != "/provider/config" {
				t.Fatalf("backend path = %s, want /provider/config", got.path)
			}
			if got.body != tt.wantBody {
				t.Fatalf("backend body = %q, want %q", got.body, tt.wantBody)
			}
		})
	}
}
