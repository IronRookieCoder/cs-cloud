package localserver

import (
	"context"
	"net/http"
	"path/filepath"
	"time"
)

type agentHealthProbeResult struct {
	Available bool   `json:"available" example:"true"`
	LatencyMs int64  `json:"latency_ms,omitempty" example:"15"`
	Error     string `json:"error,omitempty"`
}

// @Summary      List detected agents
// @Description  Detects agent backends available on the system and returns their metadata.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Router       /agents [get]
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	detected, err := s.manager.DetectAgents(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	agents := make([]map[string]any, 0, len(detected))
	for _, da := range detected {
		info := map[string]any{
			"id":        da.Backend,
			"name":      da.Name,
			"driver":    da.Driver,
			"available": da.Available,
		}
		if extra, ok := da.Extra.(map[string]any); ok {
			if ep, ok := extra["endpoint"].(string); ok {
				info["endpoint"] = ep
			}
		}
		agents = append(agents, info)
	}

	writeOK(w, map[string]any{"agents": agents})
}

// @Summary      Probe running agents health
// @Description  Checks each running agent's health by probing its /global/health endpoint. Records the workspace from X-Workspace-Directory header.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /agents/health [get]
func (s *Server) handleAgentHealth(w http.ResponseWriter, r *http.Request) {
	if dir := getWorkspaceDir(r); dir != "" {
		if abs, err := filepath.Abs(filepath.Clean(dir)); err == nil {
			s.rememberWorkspace(abs)
		}
	}

	agents := s.manager.ListAgents()
	if len(agents) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no agent running")
		return
	}

	results := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		probe := probeAgentHealth(r.Context(), a)

		entry := map[string]any{
			"id":         a.ID(),
			"backend":    a.Backend(),
			"driver":     a.Driver(),
			"state":      a.State().String(),
			"available":  probe.Available,
			"latency_ms": probe.LatencyMs,
			"error":      probe.Error,
		}

		results = append(results, entry)
	}
	writeOK(w, map[string]any{"agents": results})
}

type agentVersionEntry struct {
	Backend string `json:"backend"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// @Summary      Get agent CLI version
// @Description  Returns the version of each running agent's CLI binary. May return 404 if version information is unavailable.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      404  {object}  envelope
// @Router       /agents/version [get]
func (s *Server) handleAgentVersion(w http.ResponseWriter, r *http.Request) {
	agents := s.manager.ListAgents()
	if len(agents) == 0 {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "no agent running")
		return
	}

	results := make([]agentVersionEntry, 0, len(agents))
	for _, a := range agents {
		entry := agentVersionEntry{Backend: a.Backend()}

		if d, ok := s.manager.GetDriver(a.Backend()); ok {
			if ver, err := d.Version(); err == nil && ver != "" {
				entry.Version = ver
			} else if err != nil {
				entry.Error = err.Error()
			}
		}

		results = append(results, entry)
	}

	if len(results) == 0 || allEmptyVersion(results) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "version information unavailable")
		return
	}
	writeOK(w, map[string]any{"agents": results})
}

func allEmptyVersion(results []agentVersionEntry) bool {
	for _, r := range results {
		if r.Version != "" {
			return false
		}
	}
	return true
}

func probeAgentHealth(parent context.Context, a interface{}) agentHealthProbeResult {
	endpointAgent, ok := a.(interface{ Endpoint() string })
	if !ok || endpointAgent.Endpoint() == "" {
		return agentHealthProbeResult{Available: false, Error: "agent endpoint unavailable"}
	}

	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointAgent.Endpoint()+"/global/health", nil)
	if err != nil {
		return agentHealthProbeResult{Available: false, Error: err.Error()}
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return agentHealthProbeResult{Available: false, Error: err.Error()}
	}
	defer resp.Body.Close()

	latencyMs := time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return agentHealthProbeResult{
			Available: false,
			LatencyMs: latencyMs,
			Error:     resp.Status,
		}
	}

	return agentHealthProbeResult{Available: true, LatencyMs: latencyMs}
}
