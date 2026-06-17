package localserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// handleEventsSSE merges two event sources into a single SSE stream:
//   1) the backend agent SSE (proxied via handleProxy)
//   2) host.xxx events from the EventBus (file watcher / git watcher)
//
// When a workspace directory is specified via X-Workspace-Directory header,
// file and git watchers are registered on connect and removed on disconnect.
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "streaming not supported")
		return
	}

	// Resolve workspace directory
	workspace := resolveWorkspaceDir(r)

	// Register file/git watchers for the workspace
	if workspace != "" {
		s.registerWorkspaceWatchers(workspace)
		s.eventBus.SetActiveWorkspace(workspace)
	}

	// SSE headers
	setCORSHeaders(w.Header(), r.Header.Get("Origin"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe to EventBus for host events (no backend filter)
	hostCh := s.eventBus.Subscribe(nil)
	defer s.eventBus.Unsubscribe(hostCh)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Cleanup watchers on disconnect
	defer func() {
		if workspace != "" {
			s.unregisterWorkspaceWatchers(workspace)
		}
	}()

	// Start backend agent SSE proxy in a goroutine
	backendCh := make(chan string, 64)
	go s.proxyBackendSSE(ctx, r, backendCh)

	// Heartbeat ticker
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-backendCh:
			if !ok {
				// Backend agent disconnected (e.g. restart/shutdown).
				// Don't terminate — keep EventBus subscription alive
				// for system events so clients can be notified when
				// the new agent is ready.
				backendCh = nil
				continue
			}
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()

		case evt, ok := <-hostCh:
			if !ok {
				return
			}
			if !isHostEvent(evt.Type) && !isSystemEvent(evt.Type) {
				continue
			}

			// Filter events by workspace directory if specified
			if workspace != "" && !shouldSendEventToWorkspace(evt, workspace) {
				continue
			}

			data := serializeHostEvent(evt)
			if data == "" {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// resolveWorkspaceDir resolves the absolute workspace directory from the request.
func resolveWorkspaceDir(r *http.Request) string {
	dir := getWorkspaceDir(r)
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return ""
		}
		dir = abs
	}
	return dir
}

// registerWorkspaceWatchers starts file and git watching for the given workspace.
func (s *Server) registerWorkspaceWatchers(workspace string) {
	if s.fileWatcher != nil {
		if err := s.fileWatcher.AddDirectory(workspace); err != nil {
			logger.Warn("events SSE: failed to watch directory %s: %v", workspace, err)
		}
	}
	if s.gitWatcher != nil {
		// Only watch if it's a git repository
		gitDir := filepath.Join(workspace, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			if err := s.gitWatcher.AddRepository(workspace); err != nil {
				logger.Warn("events SSE: failed to watch git repo %s: %v", workspace, err)
			}
		}
	}
}

// unregisterWorkspaceWatchers stops file and git watching for the given workspace.
func (s *Server) unregisterWorkspaceWatchers(workspace string) {
	if s.fileWatcher != nil {
		_ = s.fileWatcher.RemoveDirectory(workspace)
	}
	if s.gitWatcher != nil {
		_ = s.gitWatcher.RemoveRepository(workspace)
	}
}

// proxyBackendSSE makes a request to the backend agent SSE endpoint and
// pipes each SSE line into the channel.
func (s *Server) proxyBackendSSE(ctx context.Context, origReq *http.Request, out chan<- string) {
	defer close(out)

	endpoint := s.manager.Endpoint()
	if endpoint == "" {
		logger.Warn("events SSE: no agent backend, host-only mode")
		return
	}

	backend := s.manager.DefaultBackend()
	d, ok := s.manager.GetDriver(backend)
	if !ok {
		logger.Warn("events SSE: no driver for backend %s", backend)
		return
	}

	// Find the rewrite rule for /events
	var target string
	cleanPath := "/events"
	for _, rt := range d.ProxyRoutes() {
		if rt.Method == http.MethodGet && matchRoute(cleanPath, rt.Prefix) {
			target = rt.Rewrite(nil)
			break
		}
	}
	if target == "" {
		logger.Warn("events SSE: no proxy route for /events")
		return
	}

	url := endpoint + target
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Error("events SSE: build request failed: %v", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Copy workspace header if present
	for from, to := range d.HeaderMap() {
		if v := origReq.Header.Get(from); v != "" {
			req.Header.Set(to, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error("events SSE: backend request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("events SSE: backend returned %d: %s", resp.StatusCode, string(body))
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		select {
		case out <- line:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Debug("events SSE: backend scanner stopped: %v", err)
	}
}

// isHostEvent returns true if the event type is a host.xxx event.
func isHostEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "host.")
}

// isSystemEvent returns true if the event type is a runtime-level system event
// that should be forwarded to all connected SSE clients regardless of workspace.
func isSystemEvent(eventType string) bool {
	return eventType == "agent.runtime.restarted"
}

// shouldSendEventToWorkspace checks if a host event should be sent to the given workspace.
// Only events that belong to the workspace (git events with matching repo_path,
// file events within the workspace directory) are allowed.
func shouldSendEventToWorkspace(evt agent.Event, workspace string) bool {
	if evt.Data == nil {
		return true // Allow events without data
	}

	data, ok := evt.Data.(map[string]any)
	if !ok {
		return true
	}

	// For git events, check repo_path
	repoPath, hasRepoPath := data["repo_path"].(string)
	if hasRepoPath && repoPath != "" {
		// Normalize paths for comparison
		normalizedRepo := filepath.Clean(repoPath)
		normalizedWorkspace := filepath.Clean(workspace)
		return normalizedRepo == normalizedWorkspace
	}

	// For file events, check if the file path is within workspace
	filePath, hasFilePath := data["path"].(string)
	if hasFilePath && filePath != "" {
		normalizedFile := filepath.Clean(filePath)
		normalizedWorkspace := filepath.Clean(workspace)
		// Check if the file is within the workspace directory
		relPath, err := filepath.Rel(normalizedWorkspace, normalizedFile)
		if err != nil {
			return false
		}
		// Check if the relative path doesn't start with ".." (meaning it's within workspace)
		return !strings.HasPrefix(relPath, "..")
	}

	// Allow other host events without specific path data
	return true
}

// serializeHostEvent converts an agent.Event into the SSE envelope format
// matching the frame() convention: {"type":"<eventType>","properties":{...}}
func serializeHostEvent(evt agent.Event) string {
	props := map[string]any{
		"timestamp": time.Now().UnixMilli(),
	}
	if evt.Data != nil {
		if m, ok := evt.Data.(map[string]any); ok {
			for k, v := range m {
				props[k] = v
			}
		}
	}

	envelope := struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}{
		Type:       evt.Type,
		Properties: props,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		logger.Error("events SSE: failed to marshal host event: %v", err)
		return ""
	}
	return string(data)
}

// handleEvents routes to the merged SSE handler when watchers are active,
// otherwise falls back to simple proxy.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// If we have host event watchers, use the merged SSE stream
	if s.fileWatcher != nil || s.gitWatcher != nil {
		s.handleEventsSSE(w, r)
		return
	}
	// No watchers — fall back to simple proxy
	s.handleProxy(w, r)
}
