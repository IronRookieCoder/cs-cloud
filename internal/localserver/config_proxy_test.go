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

type configProxyBackendRequest struct {
	method string
	path   string
	body   string
}

type configProxyDriver struct {
	endpoint string
}

func (d *configProxyDriver) Name() string { return "config-proxy" }

func (d *configProxyDriver) Detect(context.Context) ([]agent.DetectedAgent, error) {
	return nil, nil
}

func (d *configProxyDriver) CreateAgent(cfg agent.AgentConfig) (agent.Agent, error) {
	return &configProxyAgent{id: cfg.ID, endpoint: d.endpoint}, nil
}

func (d *configProxyDriver) HealthCheck(context.Context, string) (*agent.HealthResult, error) {
	return nil, nil
}

func (d *configProxyDriver) ProxyRoutes() []agent.ProxyRoute {
	return []agent.ProxyRoute{
		{Method: http.MethodGet, Prefix: "/config", Rewrite: agent.RewriteTo("/config")},
		{Method: http.MethodPatch, Prefix: "/config", Rewrite: agent.RewriteTo("/config")},
	}
}

func (d *configProxyDriver) HeaderMap() map[string]string { return map[string]string{} }

func (d *configProxyDriver) FetchCommands(string) ([]agent.SlashCommand, error) { return nil, nil }

func (d *configProxyDriver) PrewarmPaths() []string { return nil }

func (d *configProxyDriver) Version() (string, error) { return "test", nil }

type configProxyAgent struct {
	id           string
	endpoint     string
	eventEmitter func(agent.Event)
}

func (a *configProxyAgent) ID() string { return a.id }

func (a *configProxyAgent) Backend() string { return "config-proxy" }

func (a *configProxyAgent) Driver() string { return "http" }

func (a *configProxyAgent) State() agent.AgentState { return agent.StateConnected }

func (a *configProxyAgent) PID() int { return 0 }

func (a *configProxyAgent) Start(context.Context) error { return nil }

func (a *configProxyAgent) Kill() error { return nil }

func (a *configProxyAgent) SendMessage(context.Context, agent.PromptMessage) error { return nil }

func (a *configProxyAgent) CancelPrompt(context.Context) error { return nil }

func (a *configProxyAgent) ConfirmPermission(context.Context, string, string) error { return nil }

func (a *configProxyAgent) PendingPermissions() []agent.PermissionInfo { return nil }

func (a *configProxyAgent) GetModelInfo() *agent.ModelInfo { return nil }

func (a *configProxyAgent) SetModel(context.Context, string) (*agent.ModelInfo, error) {
	return nil, nil
}

func (a *configProxyAgent) SessionID() string { return "" }

func (a *configProxyAgent) SetEventEmitter(emitter func(agent.Event)) { a.eventEmitter = emitter }

func (a *configProxyAgent) Endpoint() string { return a.endpoint }

func TestConfigProxyForwardsRequestsToBackend(t *testing.T) {
	received := make(chan configProxyBackendRequest, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read backend body: %v", err)
		}
		received <- configProxyBackendRequest{
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
	srv.manager.RegisterDriver(&configProxyDriver{endpoint: backend.URL})
	if err := srv.manager.CreateAgent(context.Background(), "default", agent.AgentConfig{
		ID:         "default",
		Backend:    "config-proxy",
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
		{name: "patch", method: http.MethodPatch, body: `{"updates":{"model":"sonnet"}}`, wantBody: `{"updates":{"model":"sonnet"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/config", strings.NewReader(tt.body))
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
			if got.path != "/config" {
				t.Fatalf("backend path = %s, want /config", got.path)
			}
			if got.body != tt.wantBody {
				t.Fatalf("backend body = %q, want %q", got.body, tt.wantBody)
			}
		})
	}
}
