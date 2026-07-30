package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/runtime"
)

type pendingBuffer struct {
	timer       *time.Timer
	eventType   string         // "permission" or "question"
	payload     map[string]any // for question: single payload to forward
	permissions []map[string]any // for permission: collected permission data
	sessionID   string
	path        string
}

type NotifyForwarder struct {
	eventBus               *runtime.EventBus
	cloudClient            *Client
	deviceID               string
	deviceToken            string
	credBaseURL            string
	bufferSeconds          int // question buffer (default 60)
	permissionBufferSeconds int // permission batch window (default 5)
	idleBufferSeconds      int // idle debounce window (default 30)

	mu      sync.Mutex
	pending map[string]*pendingBuffer // key = sessionID:eventPrefix
}

func NewNotifyForwarder(eventBus *runtime.EventBus, cloudClient *Client, deviceID, deviceToken, credBaseURL string, bufferSeconds, permissionBufferSeconds, idleBufferSeconds int) *NotifyForwarder {
	if bufferSeconds <= 0 {
		bufferSeconds = 60
	}
	if permissionBufferSeconds <= 0 {
		permissionBufferSeconds = 5
	}
	if idleBufferSeconds <= 0 {
		idleBufferSeconds = 30
	}
	return &NotifyForwarder{
		eventBus:               eventBus,
		cloudClient:            cloudClient,
		deviceID:               deviceID,
		deviceToken:            deviceToken,
		credBaseURL:            credBaseURL,
		bufferSeconds:          bufferSeconds,
		permissionBufferSeconds: permissionBufferSeconds,
		idleBufferSeconds:      idleBufferSeconds,
		pending:                make(map[string]*pendingBuffer),
	}
}

func (f *NotifyForwarder) Start(ctx context.Context) {
	ch := f.eventBus.Subscribe(nil)
	logger.Info("[notify-forwarder] subscribed to event bus, waiting for events (permission=%ds, question=%ds, idle=%ds)...", f.permissionBufferSeconds, f.bufferSeconds, f.idleBufferSeconds)
	go func() {
		for {
			select {
			case <-ctx.Done():
				f.eventBus.Unsubscribe(ch)
				f.cancelAllPending()
				logger.Info("[notify-forwarder] stopped, unsubscribed from event bus")
				return
			case event, ok := <-ch:
				if !ok {
					f.cancelAllPending()
					logger.Info("[notify-forwarder] event channel closed, stopping")
					return
				}
				f.handleEvent(event)
			}
		}
	}()
}

// pendingKey builds the lookup key from sessionID and event type prefix.
func pendingKey(sessionID, eventType string) string {
	prefix := strings.SplitN(eventType, ".", 2)[0]
	return sessionID + ":" + prefix
}

func (f *NotifyForwarder) handleEvent(event agent.Event) {
	// Response events → check pending buffer first, then forward to server
	if isResponseEvent(event.Type) {
		sessionID := extractSessionID(event.Data)
		if sessionID == "" {
			sessionID = event.ConversationID
		}
		key := pendingKey(sessionID, event.Type)

		f.mu.Lock()
		if p, ok := f.pending[key]; ok {
			p.timer.Stop()
			delete(f.pending, key)
			f.mu.Unlock()
			logger.Info("[notify-forwarder] buffer cancelled by response: type=%s sessionID=%s", event.Type, sessionID)
			return
		}
		f.mu.Unlock()

		// Not a buffered event → forward response to server
		f.handleResponseEvent(event)
		return
	}

	switch event.Type {
	case "permission.asked":
		permData := buildPermissionData(event.Data)
		sessionID := extractSessionID(event.Data)
		if sessionID == "" {
			sessionID = event.ConversationID
		}
		path := f.eventBus.GetSessionCwd(sessionID)
		if path == "" {
			path = f.eventBus.GetActiveWorkspace()
		}
		f.bufferPermissionEvent(sessionID, path, permData)

	case "question.asked":
		notifyData := buildQuestionData(event.Data)
		sessionID := extractSessionID(event.Data)
		if sessionID == "" {
			sessionID = event.ConversationID
		}
		path := f.eventBus.GetSessionCwd(sessionID)
		if path == "" {
			path = f.eventBus.GetActiveWorkspace()
		}
		payload := map[string]any{
			"deviceID":  f.deviceID,
			"type":      "question",
			"sessionID": sessionID,
			"path":      path,
			"data":      notifyData,
		}
		f.bufferQuestionEvent(sessionID, payload)

	case "session.idle":
		sessionID := extractSessionID(event.Data)
		if sessionID == "" {
			sessionID = event.ConversationID
		}
		path := f.eventBus.GetSessionCwd(sessionID)
		if path == "" {
			path = f.eventBus.GetActiveWorkspace()
		}
		f.bufferIdleEvent(sessionID, path)
	}
}

// bufferPermissionEvent collects permission events in a batch window.
// Multiple permissions within the window are sent as a single permission_batch request.
func (f *NotifyForwarder) bufferPermissionEvent(sessionID, path string, permData map[string]any) {
	key := pendingKey(sessionID, "permission")

	f.mu.Lock()
	if existing, ok := f.pending[key]; ok {
		// Append to existing batch, reset timer
		existing.permissions = append(existing.permissions, permData)
		existing.timer.Stop()
		existing.timer = time.AfterFunc(time.Duration(f.permissionBufferSeconds)*time.Second, func() {
			f.flushPermissionBatch(key)
		})
		f.mu.Unlock()
		logger.Info("[notify-forwarder] appended to permission batch: sessionID=%s count=%d", sessionID, len(existing.permissions))
		return
	}
	f.mu.Unlock()

	// Create new batch
	buf := &pendingBuffer{
		eventType:   "permission",
		sessionID:   sessionID,
		path:        path,
		permissions: []map[string]any{permData},
	}
	buf.timer = time.AfterFunc(time.Duration(f.permissionBufferSeconds)*time.Second, func() {
		f.flushPermissionBatch(key)
	})

	f.mu.Lock()
	f.pending[key] = buf
	f.mu.Unlock()

	logger.Info("[notify-forwarder] started permission batch window: sessionID=%s timeout=%ds", sessionID, f.permissionBufferSeconds)
}

// flushPermissionBatch sends the collected permission batch to server.
func (f *NotifyForwarder) flushPermissionBatch(key string) {
	f.mu.Lock()
	buf, ok := f.pending[key]
	if !ok {
		f.mu.Unlock()
		return
	}
	delete(f.pending, key)
	f.mu.Unlock()

	count := len(buf.permissions)
	if count == 1 {
		// Single permission → send as individual permission event
		payload := map[string]any{
			"deviceID":  f.deviceID,
			"type":      "permission",
			"sessionID": buf.sessionID,
			"path":      buf.path,
			"data":      buf.permissions[0],
		}
		logger.Info("[notify-forwarder] forwarding single permission: sessionID=%s", buf.sessionID)
		f.sendNotify(payload)
	} else {
		// Multiple permissions → send as batch
		payload := map[string]any{
			"deviceID":  f.deviceID,
			"type":      "permission_batch",
			"sessionID": buf.sessionID,
			"path":      buf.path,
			"data": map[string]any{
				"permissions": buf.permissions,
			},
		}
		logger.Info("[notify-forwarder] forwarding permission batch: sessionID=%s count=%d", buf.sessionID, count)
		f.sendNotify(payload)
	}
}

// bufferIdleEvent debounces session.idle per session. csc emits session.idle
// at the end of every turn, so a back-to-back conversation would spam the
// cloud — each new idle resets the timer, and only when no new idle arrives
// for idleBufferSeconds is the notification actually forwarded.
func (f *NotifyForwarder) bufferIdleEvent(sessionID, path string) {
	key := pendingKey(sessionID, "idle")

	payload := map[string]any{
		"deviceID":  f.deviceID,
		"type":      "idle",
		"sessionID": sessionID,
		"path":      path,
		"data":      map[string]any{"timestamp": time.Now().UnixMilli()},
	}

	f.mu.Lock()
	if existing, ok := f.pending[key]; ok {
		existing.payload = payload
		existing.path = path
		existing.timer.Stop()
		existing.timer = time.AfterFunc(time.Duration(f.idleBufferSeconds)*time.Second, func() {
			f.flushIdle(key)
		})
		f.mu.Unlock()
		logger.Info("[notify-forwarder] idle debounced: sessionID=%s reset=%ds", sessionID, f.idleBufferSeconds)
		return
	}
	f.mu.Unlock()

	buf := &pendingBuffer{
		eventType: "idle",
		sessionID: sessionID,
		path:      path,
		payload:   payload,
	}
	buf.timer = time.AfterFunc(time.Duration(f.idleBufferSeconds)*time.Second, func() {
		f.flushIdle(key)
	})

	f.mu.Lock()
	f.pending[key] = buf
	f.mu.Unlock()

	logger.Info("[notify-forwarder] idle buffered: sessionID=%s timeout=%ds", sessionID, f.idleBufferSeconds)
}

// flushIdle sends the debounced idle notification once the quiet window elapses.
func (f *NotifyForwarder) flushIdle(key string) {
	f.mu.Lock()
	buf, ok := f.pending[key]
	if !ok {
		f.mu.Unlock()
		return
	}
	delete(f.pending, key)
	f.mu.Unlock()

	logger.Info("[notify-forwarder] idle debounce settled, forwarding: sessionID=%s", buf.sessionID)
	f.sendNotify(buf.payload)
}

// bufferQuestionEvent buffers a single question event with the longer timeout.
func (f *NotifyForwarder) bufferQuestionEvent(sessionID string, payload map[string]any) {
	key := pendingKey(sessionID, "question")

	f.mu.Lock()
	if existing, ok := f.pending[key]; ok {
		existing.timer.Stop()
		delete(f.pending, key)
	}
	f.mu.Unlock()

	timer := time.AfterFunc(time.Duration(f.bufferSeconds)*time.Second, func() {
		f.mu.Lock()
		delete(f.pending, key)
		f.mu.Unlock()

		logger.Info("[notify-forwarder] buffer timeout, forwarding: type=question sessionID=%s", sessionID)
		f.sendNotify(payload)
	})

	f.mu.Lock()
	f.pending[key] = &pendingBuffer{
		timer:     timer,
		payload:   payload,
		eventType: "question",
	}
	f.mu.Unlock()

	logger.Info("[notify-forwarder] buffered event: type=question sessionID=%s timeout=%ds", sessionID, f.bufferSeconds)
}

func (f *NotifyForwarder) cancelAllPending() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, p := range f.pending {
		p.timer.Stop()
		delete(f.pending, key)
		logger.Debug("[notify-forwarder] cancelled pending: key=%s", key)
	}
}

func (f *NotifyForwarder) sendNotify(payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("[notify-forwarder] marshal failed: %v", err)
		return
	}

	url := f.cloudClient.URL("/cloud/device/notify", f.credBaseURL)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Error("[notify-forwarder] build request failed: %v", err)
		return
	}
	f.cloudClient.SetDeviceAuthHeaders(req, f.deviceToken)

	resp, err := f.cloudClient.HTTPClient().Do(req)
	if err != nil {
		logger.Error("[notify-forwarder] send failed: %v (type=%s)", err, payload["type"])
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		logger.Warn("[notify-forwarder] server returned %d for type=%s sessionID=%s", resp.StatusCode, payload["type"], payload["sessionID"])
	} else {
		logger.Info("[notify-forwarder] forwarded successfully: type=%s sessionID=%s status=%d", payload["type"], payload["sessionID"], resp.StatusCode)
	}
}

func isResponseEvent(eventType string) bool {
	return eventType == "permission.replied" ||
		eventType == "question.replied" ||
		eventType == "question.rejected"
}

func (f *NotifyForwarder) handleResponseEvent(event agent.Event) {
	sessionID := extractSessionID(event.Data)
	if sessionID == "" {
		sessionID = event.ConversationID
	}
	eventType := strings.Split(event.Type, ".")[0]

	logger.Info("[notify-forwarder] forwarding response event: type=%s sessionID=%s", eventType, sessionID)

	payload := map[string]any{
		"sessionID": sessionID,
		"type":      eventType,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("[notify-forwarder] marshal response event failed: %v", err)
		return
	}

	url := f.cloudClient.URL("/cloud/device/notify/responded", f.credBaseURL)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Error("[notify-forwarder] build responded request failed: %v", err)
		return
	}
	f.cloudClient.SetDeviceAuthHeaders(req, f.deviceToken)

	resp, err := f.cloudClient.HTTPClient().Do(req)
	if err != nil {
		logger.Warn("[notify-forwarder] responded request failed: %v (sessionID=%s)", err, sessionID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		logger.Warn("[notify-forwarder] responded server returned %d for sessionID=%s", resp.StatusCode, sessionID)
	} else {
		logger.Info("[notify-forwarder] responded forwarded successfully: type=%s sessionID=%s status=%d", eventType, sessionID, resp.StatusCode)
	}
}

func buildPermissionData(data any) map[string]any {
	if data == nil {
		return map[string]any{}
	}
	m, ok := data.(map[string]any)
	if !ok {
		return map[string]any{"raw": data}
	}
	return m
}

func buildQuestionData(data any) map[string]any {
	if data == nil {
		return map[string]any{}
	}
	m, ok := data.(map[string]any)
	if !ok {
		return map[string]any{"raw": data}
	}
	return m
}

// extractSessionID extracts the session_id from event data.
func extractSessionID(data any) string {
	if data == nil {
		return ""
	}
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["session_id"].(string)
	return id
}

// Validate checks if the forwarder has the minimum required configuration.
func (f *NotifyForwarder) Validate() error {
	if f.deviceToken == "" {
		return fmt.Errorf("notify forwarder: device token is empty")
	}
	if f.deviceID == "" {
		return fmt.Errorf("notify forwarder: device ID is empty")
	}
	return nil
}
