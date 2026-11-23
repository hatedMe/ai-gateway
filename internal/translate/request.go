package translate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type object = map[string]any

func decodeNumbers(raw []byte, target any) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(target)
}

func rawObject(raw []byte) (map[string]json.RawMessage, error) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return v, nil
}

func only(v map[string]json.RawMessage, keys ...string) error {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	for k := range v {
		if !allowed[k] {
			return fmt.Errorf("unsupported field %q", k)
		}
	}
	return nil
}

func text(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("expected string")
	}
	err := json.Unmarshal(raw, &s)
	return s, err
}

// ConvertRequest translates an Anthropic Messages body into an OpenAI Responses body.
func convertRequest(raw []byte, model string) ([]byte, error) {
	root, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	if err = only(root, "model", "max_tokens", "messages", "system", "stream", "tools", "tool_choice", "temperature", "top_p", "metadata", "stop_sequences", "thinking", "output_config"); err != nil {
		return nil, err
	}
	var req struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools         []json.RawMessage `json:"tools"`
		Temperature   *float64          `json:"temperature"`
		TopP          *float64          `json:"top_p"`
		StopSequences []string          `json:"stop_sequences"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Model) == "" || req.MaxTokens <= 0 || len(req.Messages) == 0 {
		return nil, fmt.Errorf("model, positive max_tokens and nonempty messages are required")
	}
	if len(req.StopSequences) > 0 {
		return nil, fmt.Errorf("stop_sequences are not supported by Responses")
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1) {
		return nil, fmt.Errorf("temperature must be between 0 and 1")
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return nil, fmt.Errorf("top_p must be between 0 and 1")
	}
	if model == "" {
		model = req.Model
	}
	out := object{"model": model, "max_output_tokens": req.MaxTokens, "stream": req.Stream, "store": false}
	if err = thinkingConfig(root, req.MaxTokens, out); err != nil {
		return nil, err
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if m := root["metadata"]; len(m) > 0 {
		meta, e := rawObject(m)
		if e != nil {
			return nil, e
		}
		if e = only(meta, "user_id"); e != nil {
			return nil, e
		}
		if id, ok := meta["user_id"]; ok {
			s, e := text(id)
			if e != nil {
				return nil, e
			}
			out["metadata"] = object{"user_id": s}
		}
	}
	input := make([]object, 0)
	if sys, ok := root["system"]; ok {
		parts, e := contentParts(sys)
		if e != nil {
			return nil, fmt.Errorf("system: %w", e)
		}
		content := make([]object, 0, len(parts))
		for _, p := range parts {
			b, e := rawObject(p)
			if e != nil {
				return nil, e
			}
			if e = only(b, "type", "text", "citations"); e != nil {
				return nil, e
			}
			typ, e := text(b["type"])
			if e != nil || typ != "text" {
				return nil, fmt.Errorf("system only supports text")
			}
			s, e := text(b["text"])
			if e != nil {
				return nil, e
			}
			content = append(content, object{"type": "input_text", "text": s})
		}
		if len(content) > 0 {
			input = append(input, object{"type": "message", "role": "developer", "content": content})
		}
	}
	pending := map[string]bool{}
	seen := map[string]bool{}
	var messages []json.RawMessage
	if err = json.Unmarshal(root["messages"], &messages); err != nil {
		return nil, err
	}
	for i, msg := range req.Messages {
		fields, err := rawObject(messages[i])
		if err != nil {
			return nil, err
		}
		if err = only(fields, "role", "content"); err != nil {
			return nil, err
		}
		if msg.Role == "system" || msg.Role == "developer" {
			parts, e := contentParts(msg.Content)
			if e != nil {
				return nil, fmt.Errorf("messages[%d]: %w", i, e)
			}
			if len(parts) == 0 {
				return nil, fmt.Errorf("messages[%d]: empty instruction content", i)
			}
			content := make([]object, 0, len(parts))
			for _, part := range parts {
				block, e := rawObject(part)
				if e != nil {
					return nil, e
				}
				typ, e := text(block["type"])
				if e != nil || typ != "text" {
					return nil, fmt.Errorf("messages[%d]: instruction roles only support text", i)
				}
				converted, e := inputPart(block, "developer")
				if e != nil {
					return nil, e
				}
				content = append(content, converted)
			}
			input = append(input, object{"type": "message", "role": "developer", "content": content})
			continue
		}
		if msg.Role != "user" && msg.Role != "assistant" {
			return nil, fmt.Errorf("messages[%d]: unsupported role", i)
		}
		if len(pending) > 0 && msg.Role != "user" {
			return nil, fmt.Errorf("tool_use requires a following user tool_result")
		}
		parts, e := contentParts(msg.Content)
		if e != nil {
			return nil, e
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty message content")
		}
		content := make([]object, 0)
		flush := func() {
			if len(content) > 0 {
				input = append(input, object{"type": "message", "role": msg.Role, "content": content})
				content = make([]object, 0)
			}
		}
		for _, p := range parts {
			b, e := rawObject(p)
			if e != nil {
				return nil, e
			}
			typ, e := text(b["type"])
			if e != nil {
				return nil, e
			}
			switch typ {
			case "thinking":
				if msg.Role != "assistant" {
					return nil, fmt.Errorf("thinking history requires assistant role")
				}
				item, e := reasoningHistory(b)
				if e != nil {
					return nil, e
				}
				flush()
				if item != nil {
					input = append(input, item)
				}
			case "text", "image", "document", "search_result":
				if msg.Role == "user" && len(pending) > 0 {
					return nil, fmt.Errorf("tool results must precede user text and images")
				}
				part, e := inputPart(b, msg.Role)
				if e != nil {
					return nil, e
				}
				content = append(content, part)
			case "tool_use":
				if msg.Role != "assistant" {
					return nil, fmt.Errorf("tool_use requires assistant role")
				}
				if e = only(b, "type", "id", "name", "input"); e != nil {
					return nil, e
				}
				id, e := text(b["id"])
				if e != nil || id == "" || seen[id] {
					return nil, fmt.Errorf("invalid or duplicate tool_use id")
				}
				name, e := text(b["name"])
				if e != nil || name == "" {
					return nil, fmt.Errorf("tool_use name is required")
				}
				args, e := rawObject(b["input"])
				if e != nil {
					return nil, fmt.Errorf("tool input must be an object")
				}
				arguments, _ := json.Marshal(args)
				flush()
				input = append(input, object{"type": "function_call", "call_id": id, "name": name, "arguments": string(arguments)})
				pending[id] = true
				seen[id] = true
			case "tool_result":
				if msg.Role != "user" {
					return nil, fmt.Errorf("tool_result requires user role")
				}
				if e = only(b, "type", "tool_use_id", "content", "is_error"); e != nil {
					return nil, e
				}
				id, e := text(b["tool_use_id"])
				if e != nil || !pending[id] {
					return nil, fmt.Errorf("tool_result must match an unresolved tool_use")
				}
				result, e := toolResult(b)
				if e != nil {
					return nil, e
				}
				flush()
				input = append(input, object{"type": "function_call_output", "call_id": id, "output": result})
				delete(pending, id)
			default:
				return nil, fmt.Errorf("unsupported content block %q", typ)
			}
		}
		flush()
		if msg.Role == "user" && len(pending) > 0 {
			return nil, fmt.Errorf("missing tool_result")
		}
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("missing tool_result")
	}
	if req.Messages[len(req.Messages)-1].Role == "assistant" {
		return nil, fmt.Errorf("assistant prefill is not supported")
	}
	out["input"] = input
	names := map[string]bool{}
	tools := make([]object, 0, len(req.Tools))
	for _, rawTool := range req.Tools {
		t, e := rawObject(rawTool)
		if e != nil {
			return nil, e
		}
		if e = only(t, "name", "description", "input_schema", "strict"); e != nil {
			return nil, e
		}
		name, e := text(t["name"])
		if e != nil || name == "" || names[name] {
			return nil, fmt.Errorf("invalid or duplicate tool name")
		}
		schema, e := rawObject(t["input_schema"])
		if e != nil {
			return nil, fmt.Errorf("input_schema must be an object")
		}
		strict := false
		if r, ok := t["strict"]; ok {
			if string(r) == "null" {
				return nil, fmt.Errorf("strict must be boolean")
			}
			if e = json.Unmarshal(r, &strict); e != nil {
				return nil, e
			}
		}
		if strict {
			if e = validateStrictSchema(schema); e != nil {
				return nil, e
			}
		}
		tool := object{"type": "function", "name": name, "parameters": schema, "strict": strict}
		if d, ok := t["description"]; ok {
			s, e := text(d)
			if e != nil {
				return nil, e
			}
			tool["description"] = s
		}
		tools = append(tools, tool)
		names[name] = true
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if tc, ok := root["tool_choice"]; ok {
		t, e := rawObject(tc)
		if e != nil {
			return nil, e
		}
		if e = only(t, "type", "name", "disable_parallel_tool_use"); e != nil {
			return nil, e
		}
		typ, e := text(t["type"])
		if e != nil {
			return nil, e
		}
		if typ != "none" && len(tools) == 0 {
			return nil, fmt.Errorf("tool_choice requires tools")
		}
		switch typ {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			name, e := text(t["name"])
			if e != nil || !names[name] {
				return nil, fmt.Errorf("tool_choice names an unknown tool")
			}
			out["tool_choice"] = object{"type": "function", "name": name}
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", typ)
		}
		if d, ok := t["disable_parallel_tool_use"]; ok {
			var disabled bool
			if string(d) == "null" {
				return nil, fmt.Errorf("disable_parallel_tool_use must be boolean")
			}
			if e = json.Unmarshal(d, &disabled); e != nil {
				return nil, e
			}
			out["parallel_tool_calls"] = !disabled
		}
	}
	return json.Marshal(out)
}

func contentParts(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("content is required")
	}
	if raw[0] == '"' {
		s, e := text(raw)
		if e != nil {
			return nil, e
		}
		b, _ := json.Marshal(object{"type": "text", "text": s})
		return []json.RawMessage{b}, nil
	}
	var parts []json.RawMessage
	err := json.Unmarshal(raw, &parts)
	return parts, err
}

func inputPart(b map[string]json.RawMessage, role string) (object, error) {
	typ, _ := text(b["type"])
	if typ == "document" || typ == "search_result" {
		return documentPart(b, role)
	}
	if typ == "text" {
		if err := only(b, "type", "text", "citations"); err != nil {
			return nil, err
		}
		s, e := text(b["text"])
		if e != nil {
			return nil, e
		}
		typ = "input_text"
		if role == "assistant" {
			typ = "output_text"
		}
		if c, ok := b["citations"]; ok {
			var citations []any
			if e = json.Unmarshal(c, &citations); e != nil {
				return nil, fmt.Errorf("citations must be an array")
			}
			s += "\n[Historical citations] " + string(c)
		}
		return object{"type": typ, "text": s}, nil
	}
	if role != "user" {
		return nil, fmt.Errorf("images require user role")
	}
	if err := only(b, "type", "source", "detail"); err != nil {
		return nil, err
	}
	source, e := rawObject(b["source"])
	if e != nil {
		return nil, e
	}
	st, _ := text(source["type"])
	var imageURL string
	switch st {
	case "url":
		if e = only(source, "type", "url"); e != nil {
			return nil, e
		}
		imageURL, e = text(source["url"])
		if e != nil {
			return nil, e
		}
		u, e := url.Parse(imageURL)
		if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("invalid image URL")
		}
	case "base64":
		if e = only(source, "type", "media_type", "data"); e != nil {
			return nil, e
		}
		media, e := text(source["media_type"])
		if e != nil {
			return nil, e
		}
		switch media {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported image media_type")
		}
		data, e := text(source["data"])
		if e != nil {
			return nil, e
		}
		if _, e = base64.StdEncoding.DecodeString(data); e != nil || data == "" {
			return nil, fmt.Errorf("invalid base64 image")
		}
		imageURL = "data:" + media + ";base64," + data
	default:
		return nil, fmt.Errorf("unsupported image source")
	}
	image := object{"type": "input_image", "image_url": imageURL}
	if raw, ok := b["detail"]; ok {
		detail, err := text(raw)
		if err != nil || (detail != "auto" && detail != "low" && detail != "high") {
			return nil, fmt.Errorf("image detail must be auto, low or high")
		}
		image["detail"] = detail
	}
	return image, nil
}

func toolResult(b map[string]json.RawMessage) (any, error) {
	var isError bool
	if r, ok := b["is_error"]; ok {
		if string(r) == "null" {
			return nil, fmt.Errorf("is_error must be boolean")
		}
		if err := json.Unmarshal(r, &isError); err != nil {
			return nil, err
		}
	}
	parts := []json.RawMessage{}
	if c, ok := b["content"]; ok {
		var err error
		parts, err = contentParts(c)
		if err != nil {
			return nil, err
		}
	}
	converted := make([]object, 0, len(parts))
	texts := []string{}
	hasImage := false
	for _, p := range parts {
		block, e := rawObject(p)
		if e != nil {
			return nil, e
		}
		typ, _ := text(block["type"])
		if typ != "text" && typ != "image" && typ != "document" && typ != "search_result" {
			return nil, fmt.Errorf("unsupported tool_result content")
		}
		part, e := inputPart(block, "user")
		if e != nil {
			return nil, e
		}
		converted = append(converted, part)
		if typ == "text" {
			texts = append(texts, part["text"].(string))
		} else {
			hasImage = true
		}
	}
	if hasImage {
		if isError {
			converted = append([]object{{"type": "input_text", "text": "[tool_error]"}}, converted...)
		}
		return converted, nil
	}
	s := strings.Join(texts, "\n")
	if isError {
		s = "[tool_error] " + s
	}
	return s, nil
}
