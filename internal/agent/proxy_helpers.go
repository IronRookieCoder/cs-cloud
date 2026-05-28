package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

func RewriteTo(target string) func(map[string]string) string {
	return func(_ map[string]string) string { return target }
}

func RewriteSessionID(prefix string) func(map[string]string) string {
	return func(vals map[string]string) string {
		return prefix + vals["id"]
	}
}

func RewriteSessionIDWithSuffix(prefix, suffix string) func(map[string]string) string {
	return func(vals map[string]string) string {
		return prefix + vals["id"] + suffix
	}
}

func RewritePermReply(vals map[string]string) string {
	return "/permission/" + vals["id"] + "/reply"
}

func RewriteQuestionAction(suffix string) func(map[string]string) string {
	return func(vals map[string]string) string {
		return "/question/" + vals["id"] + suffix
	}
}

func TransformPromptBody(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		defer pw.Close()

		buf, err := io.ReadAll(body)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		trimmed := bytes.TrimSpace(buf)
		if len(trimmed) == 0 {
			_, _ = pw.Write(buf)
			return
		}

		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			_, _ = pw.Write(buf)
			return
		}
		if _, ok := payload["parts"]; ok {
			_, _ = pw.Write(buf)
			return
		}

		content, _ := payload["content"].(string)
		parts := []map[string]any{}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, map[string]any{
				"type": "text",
				"text": content,
			})
		}

		transformed := map[string]any{
			"parts": parts,
		}
		if files, ok := payload["files"]; ok {
			transformed["files"] = files
		}
		if model, ok := payload["model"]; ok {
			transformed["model"] = model
		}
		if agentVal, ok := payload["agent"]; ok {
			transformed["agent"] = agentVal
		}

		encoded, err := json.Marshal(transformed)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		_, _ = pw.Write(encoded)
	}()
	return pr
}

func RenameJSONField(from, to string) func(io.ReadCloser) io.ReadCloser {
	return RenameJSONFieldAny([]string{from}, to)
}

func RenameJSONFieldAny(from []string, to string) func(io.ReadCloser) io.ReadCloser {
	return func(body io.ReadCloser) io.ReadCloser {
		pr, pw := io.Pipe()
		go func() {
			defer body.Close()
			defer pw.Close()
			buf, err := io.ReadAll(body)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			replaced := buf
			for _, key := range from {
				next := BytesReplaceKey(buf, key, to)
				if !bytes.Equal(next, buf) {
					replaced = next
					break
				}
			}
			pw.Write(replaced)
		}()
		return pr
	}
}

func BytesReplaceKey(data []byte, from, to string) []byte {
	fromKey := []byte(`"` + from + `"`)
	toKey := []byte(`"` + to + `"`)
	result := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+len(fromKey) <= len(data) && string(data[i:i+len(fromKey)]) == string(fromKey) {
			result = append(result, toKey...)
			i += len(fromKey)
			continue
		}
		result = append(result, data[i])
		i++
	}
	return result
}
