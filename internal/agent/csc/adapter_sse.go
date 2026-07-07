package csc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

func (a *AdapterServer) wrapEventStream(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var eventName string
		var dataLines []string
		ss := &streamingState{
			blocks:       make(map[int]*blockState),
			toolUseParts: make(map[string]*toolPartMeta),
		}

		flush := func() error {
			if len(dataLines) == 0 && eventName == "" {
				return nil
			}
			joined := strings.Join(dataLines, "\n")
			var frames []sseFrame
			if strings.TrimSpace(joined) != "" {
				frames = a.adaptEventPayload(eventName, joined, ss)
			}
			if len(frames) == 0 {
				eventName = ""
				dataLines = nil
				return nil
			}
			for _, f := range frames {
				if f.data != "" {
					if _, err := io.WriteString(pw, "data: "+f.data+"\n"); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(pw, "\n"); err != nil {
					return err
				}
			}
			eventName = ""
			dataLines = nil
			return nil
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := flush(); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				if _, err := io.WriteString(pw, line+"\n"); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
				continue
			}
		}
		if err := scanner.Err(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := flush(); err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	return pr
}

func (a *AdapterServer) adaptEventPayload(eventName, joined string, ss *streamingState) []sseFrame {
	var payload map[string]any
	if err := json.Unmarshal([]byte(joined), &payload); err != nil {
		return []sseFrame{{event: eventName, data: joined}}
	}
	return a.adaptEventMap(eventName, payload, ss)
}

func (a *AdapterServer) adaptEventMap(eventName string, payload map[string]any, ss *streamingState) []sseFrame {
	sessionID := extractSessionID(payload)

	if native, _ := payload["_native_opencode"].(bool); native {
		delete(payload, "_native_opencode")
		props := map[string]any{}
		for k, v := range payload {
			props[k] = v
		}
		enrichNativeToolPart(eventName, props)
		return []sseFrame{frame(eventName, props)}
	}

	// Update agent from stored session agent
	if sessionID != "" {
		if agent := a.getSessionAgent(sessionID); agent != "" {
			ss.agent = agent
		}
	}

	switch eventName {
	case "session.created":
		normalizeSession(payload)
		return []sseFrame{frame("session.created", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.ready":
		normalizeSession(payload)
		if p, ok := payload["provider_id"].(string); ok && p != "" {
			ss.providerID = p
		}
		return []sseFrame{frame("session.updated", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.deleted":
		normalizeSession(payload)
		return []sseFrame{frame("session.deleted", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.stream_event":
		return adaptStreamEvent(ss, sessionID, payload)

	case "session.message":
		return a.adaptMessageEvent(sessionID, payload, ss)

	case "session.result":
		return adaptResultEvent(sessionID, payload, ss)

	case "session.control_request":
		return adaptControlRequestEvent(sessionID, payload)

	case "session.permission_replied":
		requestID, _ := adapterString(payload["request_id"])
		return []sseFrame{frame("permission.replied", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
		})}

	case "session.question_replied":
		requestID, _ := adapterString(payload["request_id"])
		return []sseFrame{frame("question.replied", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
		})}

	default:
		typeName, _ := payload["type"].(string)
		if typeName == "" {
			typeName = eventName
		}
		if typeName == "server.heartbeat" || typeName == "server.connected" {
			return []sseFrame{frame(typeName, map[string]any{})}
		}

		if inner, _ := payload["properties"].(map[string]any); inner != nil {
			return a.adaptEventMap(typeName, inner, ss)
		}

		props := map[string]any{}
		for k, v := range payload {
			props[k] = v
		}
		if sessionID != "" {
			props["sessionID"] = sessionID
		}
		return []sseFrame{frame(typeName, props)}
	}
}

func adaptResultEvent(sessionID string, payload map[string]any, ss *streamingState) []sseFrame {
	isInterrupted, _ := payload["is_interrupted"].(bool)
	var frames []sseFrame

	if isInterrupted {
		now := time.Now().UnixMilli()
		needStepFinish := ss.active

		for toolID, meta := range ss.toolUseParts {
			frames = append(frames, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        meta.partID,
					"sessionID": sessionID,
					"messageID": meta.msgID,
					"type":      "tool",
					"callID":    toolID,
					"tool":      meta.toolName,
					"state": map[string]any{
						"status": "error",
						"error":  "Tool execution aborted",
						"input":  meta.input,
						"title":  meta.toolName,
						"time":   map[string]any{"start": now, "end": now},
					},
				},
				"time": now,
			}))
		}

		if ss.msgID != "" {
			if needStepFinish {
				frames = append(frames, frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part": map[string]any{
						"id":        fmt.Sprintf("prt_%d_finish", ss.partSeq),
						"sessionID": sessionID,
						"messageID": ss.msgID,
						"type":      "step-finish",
						"reason":    "stop",
					},
					"time": now,
				}))
			}
			frames = append(frames, frame("message.updated", map[string]any{
				"sessionID": sessionID,
				"info": map[string]any{
					"id":         ss.msgID,
					"sessionID":  sessionID,
					"role":       "assistant",
					"parentID":   ss.parentMsgID,
					"time":       map[string]any{"created": now, "completed": now},
					"modelID":    ss.modelID,
					"providerID": ss.providerID,
					"mode":       ss.agent,
					"agent":      ss.agent,
					"path":       map[string]any{"cwd": sessionID, "root": sessionID},
					"cost":       0,
					"tokens": map[string]any{
						"input": 0, "output": 0, "reasoning": 0,
						"cache": map[string]any{"read": 0, "write": 0},
					},
					"error": map[string]any{
						"name": "MessageAbortedError",
						"data": map[string]any{"message": "Aborted"},
					},
				},
			}))
			frames = append(frames, frame("session.error", map[string]any{
				"sessionID": sessionID,
				"error": map[string]any{
					"name": "MessageAbortedError",
					"data": map[string]any{"message": "Aborted"},
				},
			}))
		}
	}

	ss.active = false
	ss.blocks = make(map[int]*blockState)
	ss.toolUseParts = make(map[string]*toolPartMeta)
	ss.turnParentID = ""

	// Note: csc owns the idle event chain (session.status{idle},
	// session.idle, session.diff) and emits them based on its own
	// state machine (e.g. skipping them on compact-turn boundaries
	// to avoid transient idle flicker). cs-cloud is a transparent
	// proxy here — do not synthesize idle frames.
	return frames
}

// enrichNativeToolPart supplements tool parts emitted by csc's _native_opencode
// path.  It normalizes snake_case input keys for all tools and adds filediff
// metadata for edit/write tools before forwarding to the UI.
func enrichNativeToolPart(eventName string, props map[string]any) {
	if eventName != "message.part.updated" {
		return
	}
	part, _ := props["part"].(map[string]any)
	if part == nil || part["type"] != "tool" {
		return
	}
	state, _ := part["state"].(map[string]any)
	if state == nil {
		return
	}

	input, _ := state["input"].(map[string]any)
	if input == nil {
		return
	}
	// Normalize snake_case keys that csc may not have renamed yet.
	if v, ok := input["file_path"]; ok {
		delete(input, "file_path")
		input["filePath"] = v
	}
	if v, ok := input["old_string"]; ok {
		delete(input, "old_string")
		input["oldString"] = v
	}
	if v, ok := input["new_string"]; ok {
		delete(input, "new_string")
		input["newString"] = v
	}
	if v, ok := input["replace_all"]; ok {
		delete(input, "replace_all")
		input["replaceAll"] = v
	}

	tool, _ := part["tool"].(string)
	toolLower := strings.ToLower(tool)
	if toolLower != "edit" && toolLower != "write" {
		return
	}

	metadata, _ := state["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, exists := metadata["filediff"]; exists {
		return
	}

	status, _ := state["status"].(string)
	if filediff := buildFileDiff(input); filediff != nil {
		metadata["filediff"] = filediff
		if status == "completed" || status == "error" {
			if output, _ := state["output"].(string); output != "" {
				metadata["output"] = output
			}
		}
		state["metadata"] = metadata
	}
}

func adaptControlRequestEvent(sessionID string, payload map[string]any) []sseFrame {
	request, _ := payload["request"].(map[string]any)
	requestID, _ := adapterString(payload["request_id"])
	if request == nil {
		request = map[string]any{}
	}
	subtype, _ := request["subtype"].(string)

	switch subtype {
	case "can_use_tool":
		toolName, _ := request["tool_name"].(string)
		toolUseID, _ := request["tool_use_id"].(string)
		input := request["input"]
		if input == nil {
			input = map[string]any{}
		}
		patterns := extractPatterns(toolName, input)
		permissionKey := toPermissionKey(toolName)
		return []sseFrame{frame("permission.asked", map[string]any{
			"id":         requestID,
			"sessionID":  sessionID,
			"permission": permissionKey,
			"patterns":   patterns,
			"metadata":   map[string]any{"input": input},
			"always":     []string{},
			"tool": map[string]any{
				"messageID": "",
				"callID":    toolUseID,
			},
		})}

	case "elicitation":
		message, _ := request["message"].(string)
		serverName, _ := request["mcp_server_name"].(string)
		schema := request["requested_schema"]
		if schema == nil {
			schema = map[string]any{}
		}
		return []sseFrame{frame("question.asked", map[string]any{
			"id":        requestID,
			"sessionID": sessionID,
			"questions": []map[string]any{
				{
					"question": message,
					"header":   serverName,
					"options":  []map[string]any{},
					"multiple": false,
					"custom":   true,
				},
			},
			"tool": nil,
		})}

	default:
		return []sseFrame{frame("session.control_request", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
			"request":   request,
		})}
	}
}

func extractPatterns(toolName string, input any) []string {
	m, ok := input.(map[string]any)
	if !ok {
		return []string{}
	}
	var patterns []string
	for _, key := range []string{"file_path", "path", "pattern", "glob"} {
		if v, ok := adapterString(m[key]); ok && v != "" {
			patterns = append(patterns, v)
		}
	}
	if v, ok := adapterString(m["command"]); ok && v != "" {
		patterns = append(patterns, v)
	}
	if len(patterns) == 0 {
		return []string{}
	}
	return patterns
}

func toPermissionKey(toolName string) string {
	var knownPascal = map[string]string{
		"Read": "read", "Edit": "edit", "Write": "edit",
		"Glob": "glob", "Grep": "grep", "LS": "list",
		"Bash": "bash", "PowerShell": "bash",
		"Agent": "task",
		"WebFetch": "webfetch", "WebSearch": "websearch",
		"TodoRead": "todoread", "TodoWrite": "todowrite",
	}
	if v, ok := knownPascal[toolName]; ok {
		return v
	}
	return strings.ToLower(toolName)
}
