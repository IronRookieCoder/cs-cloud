package csc

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

func (a *AdapterServer) adaptMessageEvent(sessionID string, payload map[string]any, ss *streamingState) []sseFrame {
	msgType, _ := payload["type"].(string)

	if msgType == "user" {
		return a.adaptUserMessageEvent(sessionID, payload, ss)
	}

	if msgType != "assistant" {
		return nil
	}

	if ss.active && ss.sessionID == sessionID {
		now := time.Now().UnixMilli()
		var cost any = 0
		if c, ok := payload["cost_usd"]; ok {
			cost = c
		}
		tokens := map[string]any{
			"input": 0, "output": 0, "reasoning": 0,
			"cache": map[string]any{"read": 0, "write": 0},
		}
		if usage, ok := payload["usage"].(map[string]any); ok {
			tokens = map[string]any{
				"input":     usage["input_tokens"],
				"output":    usage["output_tokens"],
				"reasoning": 0,
				"cache":     map[string]any{"read": 0, "write": 0},
			}
		}
		infoCompleted := map[string]any{
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
			"cost":       cost,
			"tokens":     tokens,
			"finish":     "stop",
		}
		if m, ok := payload["model"].(string); ok && m != "" {
			infoCompleted["modelID"] = m
			ss.modelID = m
		}
		if p := extractProviderID(payload); p != "" {
			infoCompleted["providerID"] = p
			ss.providerID = p
		}

		result := []sseFrame{}

		if msg, ok := payload["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				for i, block := range content {
					blockMap, ok := block.(map[string]any)
					if !ok {
						continue
					}
					blockType, _ := blockMap["type"].(string)
					if blockType != "tool_use" {
						continue
					}
					toolName := normalizeToolName(blockMap["name"].(string))
					toolID, _ := blockMap["id"].(string)
					if toolID == "" {
						continue
					}
					if _, exists := ss.toolUseParts[toolID]; exists {
						continue
					}
					input := blockMap["input"]
					inputMap, _ := input.(map[string]any)
					inputMap = normalizeToolInput(inputMap)
					partID := fmt.Sprintf("prt_%d_%d", ss.partSeq, i)
					ss.toolUseParts[toolID] = &toolPartMeta{partID: partID, msgID: ss.msgID, toolName: toolName, input: inputMap}
					result = append(result, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": ss.msgID,
							"type":      "tool",
							"callID":    toolID,
							"tool":      toolName,
							"state": map[string]any{
								"status": "running",
								"input":  input,
								"title":  toolName,
							},
						},
						"time": now,
					}))
				}
			}
		}

		result = append(result, frame("message.part.updated", map[string]any{
			"sessionID": sessionID,
			"part": map[string]any{
				"id":        fmt.Sprintf("prt_%d_finish", ss.partSeq),
				"sessionID": sessionID,
				"messageID": ss.msgID,
				"type":      "step-finish",
				"reason":    "stop",
				"cost":      cost,
				"tokens":    tokens,
			},
			"time": now,
		}))
		result = append(result, frame("message.updated", map[string]any{
			"sessionID": sessionID,
			"info":      infoCompleted,
		}))

		ss.active = false
		ss.blocks = make(map[int]*blockState)
		return result
	}

	seq := atomic.AddUint64(&eventSeq, 1)
	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%d", seq)

	var userMsgID string
	if ss.turnParentID != "" {
		userMsgID = ss.turnParentID
	} else if umid, ok := adapterString(payload["parent_tool_use_id"]); ok && umid != "" {
		userMsgID = umid
	}
	if userMsgID == "" {
		userMsgID = fmt.Sprintf("msg_%d", seq-1)
		ss.turnParentID = userMsgID
	}

	info := map[string]any{
		"id":         msgID,
		"sessionID":  sessionID,
		"role":       "assistant",
		"parentID":   userMsgID,
		"time":       map[string]any{"created": now},
		"modelID":    "",
		"providerID": extractProviderID(payload),
		"mode":       ss.agent,
		"agent":      ss.agent,
		"path":       map[string]any{"cwd": sessionID, "root": sessionID},
		"cost":       0,
		"tokens": map[string]any{
			"input": 0, "output": 0, "reasoning": 0,
			"cache": map[string]any{"read": 0, "write": 0},
		},
	}

	var parts []sseFrame

	if msg, ok := payload["message"].(map[string]any); ok {
		if cost, ok := payload["cost_usd"]; ok {
			info["cost"] = cost
		}
		if usage, ok := payload["usage"].(map[string]any); ok {
			inTokens := usage["input_tokens"]
			outTokens := usage["output_tokens"]
			totalTokens := 0
			if in, ok := inTokens.(float64); ok {
				totalTokens += int(in)
			}
			if out, ok := outTokens.(float64); ok {
				totalTokens += int(out)
			}
			info["tokens"] = map[string]any{
				"total":     totalTokens,
				"input":     inTokens,
				"output":    outTokens,
				"reasoning": 0,
				"cache":     map[string]any{"read": 0, "write": 0},
			}
		}
		if model, ok := payload["model"].(string); ok {
			info["modelID"] = model
		}

		content, contentOK := msg["content"].([]any)
		if contentOK {
			partSeq := atomic.AddUint64(&eventSeq, 1)

			parts = append(parts, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_start", partSeq),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "step-start",
				},
				"time": now,
			}))

			for i, block := range content {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blockMap["type"].(string)
				partID := fmt.Sprintf("prt_%d_%d", partSeq, i)

				switch blockType {
				case "text":
					text, _ := blockMap["text"].(string)
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "text",
							"text":      text,
							"time":      map[string]any{"start": now, "end": now},
						},
						"time": now,
					}))
				case "tool_use":
					toolName := normalizeToolName(blockMap["name"].(string))
					toolID, _ := blockMap["id"].(string)
					input := blockMap["input"]
					inputMap, _ := input.(map[string]any)
					inputMap = normalizeToolInput(inputMap)
					ss.toolUseParts[toolID] = &toolPartMeta{partID: partID, msgID: msgID, toolName: toolName, input: inputMap}
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "tool",
							"callID":    toolID,
							"tool":      toolName,
							"state": map[string]any{
								"status": "running",
								"input":  inputMap,
								"title":  toolName,
							},
						},
						"time": now,
					}))
					toolLower := strings.ToLower(toolName)
					if toolLower == "task" || toolLower == "agent" {
						subagentType, _ := inputMap["subagent_type"].(string)
						if subagentType == "" {
							subagentType = "general-purpose"
						}
						desc, _ := inputMap["description"].(string)
						prompt, _ := inputMap["prompt"].(string)
						if prompt == "" {
							prompt = desc
						}
						parts = append(parts, frame("message.part.updated", map[string]any{
							"sessionID": sessionID,
							"part": map[string]any{
								"id":          fmt.Sprintf("%s_subtask", partID),
								"sessionID":   sessionID,
								"messageID":   msgID,
								"type":        "subtask",
								"prompt":      prompt,
								"description": desc,
								"agent":       subagentType,
							},
							"time": now,
						}))
					}
				case "thinking":
					thinking, _ := blockMap["thinking"].(string)
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "reasoning",
							"thinking":  thinking,
							"text":      thinking,
							"time":      map[string]any{"start": now, "end": now},
						},
						"time": now,
					}))
				}
			}

			parts = append(parts, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_finish", partSeq),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "step-finish",
					"reason":    "stop",
					"cost":      info["cost"],
					"tokens":    info["tokens"],
				},
				"time": now,
			}))
		}
	} else if msgType == "tool_progress" {
		partSeq := atomic.AddUint64(&eventSeq, 1)
		partID := fmt.Sprintf("prt_%d_tool", partSeq)
		parts = append(parts, frame("message.part.updated", map[string]any{
			"sessionID": sessionID,
			"part": map[string]any{
				"id":        partID,
				"sessionID": sessionID,
				"messageID": msgID,
				"type":      "tool",
			},
			"time": now,
		}))
	}

	result := []sseFrame{
		frame("session.status", map[string]any{
			"sessionID": sessionID,
			"status":    map[string]any{"type": "busy"},
		}),
		frame("message.updated", map[string]any{
			"sessionID": sessionID,
			"info":      info,
		}),
	}
	result = append(result, parts...)

	infoCompleted := map[string]any{}
	for k, v := range info {
		infoCompleted[k] = v
	}
	infoCompleted["time"] = map[string]any{"created": now, "completed": now + 1}
	if _, ok := info["tokens"]; ok {
		infoCompleted["finish"] = "stop"
	}

	result = append(result, frame("message.updated", map[string]any{
		"sessionID": sessionID,
		"info":      infoCompleted,
	}))

	return result
}

func (a *AdapterServer) adaptUserMessageEvent(sessionID string, payload map[string]any, ss *streamingState) []sseFrame {
	if isMeta, _ := payload["isMeta"].(bool); isMeta {
		return nil
	}
	msg, _ := payload["message"].(map[string]any)
	if msg != nil {
		if content, ok := msg["content"].(string); ok {
			if content == "[Request interrupted by user]" || content == "[Request interrupted by user for tool use]" {
				return nil
			}
			if containsLocalCommandTags(content) {
				return nil
			}
		}
		if blocks, ok := msg["content"].([]any); ok {
			for _, block := range blocks {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if blockMap["type"] == "text" {
					text, _ := blockMap["text"].(string)
					if text == "[Request interrupted by user]" || text == "[Request interrupted by user for tool use]" {
						return nil
					}
					if containsLocalCommandTags(text) {
						return nil
					}
				}
			}
		}
	}

	seq := atomic.AddUint64(&eventSeq, 1)
	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%d", seq)
	if uuid, ok := payload["uuid"].(string); ok && uuid != "" {
		msgID = uuid
	}

	if msg == nil {
		msg, _ = payload["message"].(map[string]any)
	}
	var contentBlocks []any
	if msg != nil {
		contentBlocks, _ = msg["content"].([]any)
	}

	hasText := false
	hasToolResult := false
	for _, block := range contentBlocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)
		if blockType == "text" {
			hasText = true
		}
		if blockType == "tool_result" {
			hasToolResult = true
		}
	}

	result := []sseFrame{}

	if hasToolResult && !hasText {
		for _, block := range contentBlocks {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if blockType != "tool_result" {
				continue
			}
			toolUseID, _ := blockMap["tool_use_id"].(string)
			if toolUseID == "" {
				continue
			}
			meta, tracked := ss.toolUseParts[toolUseID]
			if !tracked {
				continue
			}
			output := extractToolResultContent(blockMap)
			isError, _ := blockMap["is_error"].(bool)
			state := toolCompletedState(meta, output, isError)
			result = append(result, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        meta.partID,
					"sessionID": sessionID,
					"messageID": meta.msgID,
					"type":      "tool",
					"callID":    toolUseID,
					"tool":      meta.toolName,
					"state":     state,
				},
				"time": now,
			}))
		}
		return result
	}

	ss.turnParentID = msgID

	var modelID string
	if m, ok := payload["model"].(string); ok && m != "" {
		modelID = m
	} else {
		modelID = ss.modelID
	}
	providerID := extractProviderID(payload)
	if providerID == "" {
		providerID = ss.providerID
	}

	info := map[string]any{
		"id":        msgID,
		"sessionID": sessionID,
		"role":      "user",
		"time":      map[string]any{"created": now},
		"agent":     ss.agent,
	}
	if modelID != "" || providerID != "" {
		info["model"] = map[string]any{"providerID": providerID, "modelID": modelID}
	}

	result = append(result, frame("message.updated", map[string]any{
		"sessionID": sessionID,
		"info":      info,
	}))

	if rawParts, ok := payload["parts"].([]any); ok && len(rawParts) > 0 {
		partIdx := 0
		for _, rp := range rawParts {
			pm, ok := rp.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := pm["type"].(string)
			switch partType {
		case "text":
			text, _ := pm["text"].(string)
			result = append(result, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_%d", seq, partIdx),
					"type":      "text",
					"text":      text,
					"messageID": msgID,
					"sessionID": sessionID,
				},
				"time": now,
			}))
			if meta, ok := pm["metadata"].(map[string]any); ok {
				if ws, ok := meta["_workspace"].(map[string]any); ok {
					if src, ok := ws["source"].(map[string]any); ok {
						dir, _ := ws["directory"].(string)
						name, _ := ws["workspaceName"].(string)
						wsStart, _ := src["start"].(float64)
						wsEnd, _ := src["end"].(float64)
						wsValue, _ := src["value"].(string)
						if wsStart > 0 || wsEnd > 0 {
							result = append(result, frame("message.part.updated", map[string]any{
								"sessionID": sessionID,
								"part": map[string]any{
									"id":        fmt.Sprintf("prt_%d_ws_%d", seq, partIdx),
									"sessionID": sessionID,
									"messageID": msgID,
									"type":      "file",
									"mime":      "text/directory",
									"url":       "file://" + dir,
									"filename":  name,
									"source": map[string]any{
										"type": "file",
										"text": map[string]any{
											"value": wsValue,
											"start": int(wsStart),
											"end":   int(wsEnd),
										},
										"path": dir,
									},
								},
								"time": now,
							}))
						}
					}
				}
			}
			case "agent":
				agentPart := map[string]any{
					"id":        fmt.Sprintf("prt_%d_%d", seq, partIdx),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "agent",
				}
				for k, v := range pm {
					if k != "type" && k != "id" {
						agentPart[k] = v
					}
				}
				result = append(result, frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part":      agentPart,
					"time":      now,
				}))
			case "file":
				filePart := map[string]any{
					"id":        fmt.Sprintf("prt_%d_%d", seq, partIdx),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "file",
				}
				for k, v := range pm {
					if k != "type" && k != "id" {
						filePart[k] = v
					}
				}
				result = append(result, frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part":      filePart,
					"time":      now,
				}))
			}
			partIdx++
		}
		return result
	}

	if contentBlocks == nil {
		if c, ok := payload["content"].(string); ok {
			result = append(result, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_0", seq),
					"type":      "text",
					"text":      c,
					"messageID": msgID,
					"sessionID": sessionID,
				},
				"time": now,
			}))
			return result
		}
	}

	for _, block := range contentBlocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)
		switch blockType {
		case "text":
			text, _ := blockMap["text"].(string)
			result = append(result, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_txt", seq),
					"type":      "text",
					"text":      text,
					"messageID": msgID,
					"sessionID": sessionID,
				},
				"time": now,
			}))
		case "tool_result":
			toolUseID, _ := blockMap["tool_use_id"].(string)
			if toolUseID == "" {
				continue
			}
			meta, tracked := ss.toolUseParts[toolUseID]
			if !tracked {
				continue
			}
			output := extractToolResultContent(blockMap)
			isError, _ := blockMap["is_error"].(bool)
			state := toolCompletedState(meta, output, isError)
			result = append(result, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        meta.partID,
					"sessionID": sessionID,
					"messageID": meta.msgID,
					"type":      "tool",
					"callID":    toolUseID,
					"tool":      meta.toolName,
					"state":     state,
				},
				"time": now,
			}))
		}
	}

	if cached, ok := a.pendingFiles.LoadAndDelete(sessionID); ok {
		if fileParts, ok := cached.([]map[string]any); ok {
			for i, fp := range fileParts {
				filePart := map[string]any{
					"id":        fmt.Sprintf("prt_%d_file_%d", seq, i),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "file",
				}
				for k, v := range fp {
					if k != "type" && k != "id" {
						filePart[k] = v
					}
				}
				if source, ok := fp["source"].(map[string]any); ok {
					filePart["source"] = source
				}
				result = append(result, frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part":      filePart,
					"time":      now,
				}))
			}
		}
	}

	return result
}

func extractToolResultContent(block map[string]any) string {
	content := block["content"]
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content != nil {
			b, _ := json.Marshal(content)
			return string(b)
		}
		return ""
	}
}
