package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// PrepareChatRequest reuses the validated Messages -> Responses conversion.
func PrepareChatRequest(raw []byte, options RequestOptions) (PreparedRequest, bool, error) {
	var empty PreparedRequest
	root, err := rawObject(raw)
	if err != nil {
		return empty, false, err
	}
	if err = only(root, "model", "messages", "max_tokens", "max_completion_tokens", "stream", "stream_options", "temperature", "top_p", "tools", "tool_choice", "parallel_tool_calls", "stop", "reasoning_effort", "response_format", "n", "user", "metadata", "service_tier", "store", "seed", "presence_penalty", "frequency_penalty", "logprobs", "top_logprobs", "logit_bias", "safety_identifier", "prompt_cache_key", "prompt_cache_retention"); err != nil {
		return empty, false, err
	}
	warnings := []string{}
	warn := func(s string) { warnings = append(warnings, s) }
	var includeUsage bool
	if r, ok := root["stream_options"]; ok && string(r) != "null" {
		v, e := rawObject(r)
		if e != nil {
			return empty, false, e
		}
		if e = only(v, "include_usage"); e != nil {
			return empty, false, e
		}
		if r, ok := v["include_usage"]; ok {
			if e = json.Unmarshal(r, &includeUsage); e != nil || string(r) == "null" {
				return empty, false, fmt.Errorf("stream_options.include_usage must be boolean")
			}
		}
	}
	if r, ok := root["n"]; ok {
		var n int
		if json.Unmarshal(r, &n) != nil || n != 1 {
			return empty, false, fmt.Errorf("only n=1 is supported by Responses")
		}
	}
	intermediate := object{}
	for _, k := range []string{"model", "stream", "top_p"} {
		if r, ok := root[k]; ok {
			intermediate[k] = r
		}
	}
	maxTokens := 4096
	limit, exists := root["max_completion_tokens"]
	if !exists {
		limit, exists = root["max_tokens"]
	}
	if exists {
		if json.Unmarshal(limit, &maxTokens) != nil || maxTokens <= 0 {
			return empty, false, fmt.Errorf("token limit must be a positive integer")
		}
	}
	intermediate["max_tokens"] = maxTokens
	if r, ok := root["stop"]; ok && string(r) != "null" {
		var stop string
		if json.Unmarshal(r, &stop) == nil {
			intermediate["stop_sequences"] = []string{stop}
		} else {
			var stops []string
			if json.Unmarshal(r, &stops) != nil {
				return empty, false, fmt.Errorf("stop must be a string or string array")
			}
			intermediate["stop_sequences"] = stops
		}
	}
	if r, ok := root["reasoning_effort"]; ok && string(r) != "null" {
		effort, e := text(r)
		if e != nil {
			return empty, false, e
		}
		if effort == "none" {
			intermediate["thinking"] = object{"type": "disabled"}
		} else {
			intermediate["output_config"] = object{"effort": effort}
		}
	}
	var messages []json.RawMessage
	if err = json.Unmarshal(root["messages"], &messages); err != nil || len(messages) == 0 {
		return empty, false, fmt.Errorf("nonempty messages are required")
	}
	translated := []object{}
	previousTool := false
	for i, rawMessage := range messages {
		m, e := rawObject(rawMessage)
		if e != nil {
			return empty, false, e
		}
		if e = only(m, "role", "content", "name", "tool_calls", "tool_call_id", "refusal", "reasoning_content", "reasoning_blocks", "citations", "annotations"); e != nil {
			return empty, false, fmt.Errorf("messages[%d]: %w", i, e)
		}
		role, e := text(m["role"])
		if e != nil {
			return empty, false, e
		}
		if _, ok := m["name"]; ok {
			warn("message.name has no equivalent and was omitted")
		}
		if role == "tool" {
			id, e := text(m["tool_call_id"])
			if e != nil || id == "" {
				return empty, false, fmt.Errorf("tool message requires tool_call_id")
			}
			parts, e := chatContent(m["content"])
			if e != nil {
				return empty, false, e
			}
			result := object{"type": "tool_result", "tool_use_id": id, "content": parts}
			if previousTool {
				last := translated[len(translated)-1]
				last["content"] = append(last["content"].([]any), result)
			} else {
				translated = append(translated, object{"role": "user", "content": []any{result}})
			}
			previousTool = true
			continue
		}
		previousTool = false
		if role != "user" && role != "assistant" && role != "system" && role != "developer" {
			return empty, false, fmt.Errorf("messages[%d]: unsupported role %q", i, role)
		}
		content, e := chatContent(m["content"])
		if e != nil {
			return empty, false, e
		}
		for _, key := range []string{"citations", "annotations"} {
			if refs, ok := m[key]; ok && string(refs) != "null" {
				var values []json.RawMessage
				if e = json.Unmarshal(refs, &values); e != nil {
					return empty, false, fmt.Errorf("message.%s must be an array", key)
				}
				if len(values) > 0 {
					content = append(content, object{"type": "text", "text": "[Chat " + key + "] " + string(refs)})
					warn("Chat citation history retained as readable context")
				}
			}
		}
		if r, ok := m["refusal"]; ok && string(r) != "null" {
			s, e := text(r)
			if e != nil {
				return empty, false, e
			}
			if s != "" {
				content = append(content, object{"type": "text", "text": s})
			}
		}
		if r, ok := m["reasoning_blocks"]; ok {
			if role != "assistant" {
				return empty, false, fmt.Errorf("reasoning_blocks require assistant role")
			}
			var blocks []object
			if e = json.Unmarshal(r, &blocks); e != nil {
				return empty, false, e
			}
			prefix := []any{}
			for _, b := range blocks {
				if b["type"] != "thinking" {
					return empty, false, fmt.Errorf("reasoning_blocks must contain thinking blocks")
				}
				prefix = append(prefix, b)
			}
			content = append(prefix, content...)
		} else if r, ok := m["reasoning_content"]; ok && string(r) != "null" {
			if _, e = text(r); e != nil {
				return empty, false, e
			}
			warn("reasoning_content without reasoning_blocks cannot restore encrypted reasoning and was omitted")
		}
		if r, ok := m["tool_calls"]; ok {
			if role != "assistant" {
				return empty, false, fmt.Errorf("tool_calls require assistant role")
			}
			var calls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if e = json.Unmarshal(r, &calls); e != nil {
				return empty, false, e
			}
			for _, call := range calls {
				if call.Type != "function" || call.ID == "" || call.Function.Name == "" {
					return empty, false, fmt.Errorf("invalid function tool call")
				}
				input, e := rawObject([]byte(call.Function.Arguments))
				if e != nil {
					return empty, false, fmt.Errorf("tool call arguments must be a JSON object: %w", e)
				}
				content = append(content, object{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
			}
		}
		if len(content) == 0 {
			return empty, false, fmt.Errorf("messages[%d]: empty content", i)
		}
		translated = append(translated, object{"role": role, "content": content})
	}
	intermediate["messages"] = translated
	if r, ok := root["tools"]; ok {
		var tools []json.RawMessage
		if err = json.Unmarshal(r, &tools); err != nil {
			return empty, false, err
		}
		converted := []object{}
		for _, rawTool := range tools {
			t, e := rawObject(rawTool)
			if e != nil {
				return empty, false, e
			}
			if e = only(t, "type", "function"); e != nil {
				return empty, false, e
			}
			typ, e := text(t["type"])
			if e != nil || typ != "function" {
				return empty, false, fmt.Errorf("only function tools are supported by Chat adapter")
			}
			f, e := rawObject(t["function"])
			if e != nil {
				return empty, false, e
			}
			if e = only(f, "name", "description", "parameters", "strict"); e != nil {
				return empty, false, e
			}
			tool := object{}
			for _, k := range []string{"name", "description", "strict"} {
				if r, ok := f[k]; ok {
					tool[k] = r
				}
			}
			if parameters, ok := f["parameters"]; ok {
				tool["input_schema"] = parameters
			} else {
				tool["input_schema"] = object{"type": "object", "properties": object{}}
			}
			converted = append(converted, tool)
		}
		intermediate["tools"] = converted
	}
	choice := object{"type": "auto"}
	hasChoice := false
	if r, ok := root["tool_choice"]; ok && string(r) != "null" {
		hasChoice = true
		var mode string
		if json.Unmarshal(r, &mode) == nil {
			switch mode {
			case "auto", "none":
				choice["type"] = mode
			case "required":
				choice["type"] = "any"
			default:
				return empty, false, fmt.Errorf("unsupported tool_choice %q", mode)
			}
		} else {
			var selected struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(r, &selected) != nil || selected.Type != "function" || selected.Function.Name == "" {
				return empty, false, fmt.Errorf("invalid tool_choice")
			}
			choice = object{"type": "tool", "name": selected.Function.Name}
		}
	}
	if r, ok := root["parallel_tool_calls"]; ok {
		var parallel bool
		if json.Unmarshal(r, &parallel) != nil || string(r) == "null" {
			return empty, false, fmt.Errorf("parallel_tool_calls must be boolean")
		}
		choice["disable_parallel_tool_use"] = !parallel
		hasChoice = true
	}
	if hasChoice {
		tools, _ := intermediate["tools"].([]object)
		if len(tools) > 0 || (choice["type"] != "auto" && choice["type"] != "none") {
			intermediate["tool_choice"] = choice
		}
	}
	b, err := json.Marshal(intermediate)
	if err != nil {
		return empty, false, err
	}
	prepared, err := PrepareRequest(b, options)
	if err != nil {
		return empty, false, err
	}
	var out object
	_ = decodeNumbers(prepared.Body, &out)
	if r, ok := root["temperature"]; ok && string(r) != "null" {
		var n float64
		if json.Unmarshal(r, &n) != nil || n < 0 || n > 2 {
			return empty, false, fmt.Errorf("temperature must be between 0 and 2")
		}
		out["temperature"] = n
	}
	for _, k := range []string{"metadata", "service_tier", "safety_identifier", "prompt_cache_key", "prompt_cache_retention"} {
		if r, ok := root[k]; ok && string(r) != "null" {
			out[k] = r
		}
	}
	if r, ok := root["user"]; ok && string(r) != "null" {
		user, e := text(r)
		if e != nil {
			return empty, false, e
		}
		if _, exists := out["safety_identifier"]; !exists {
			out["safety_identifier"] = user
		}
	}
	if r, ok := root["response_format"]; ok && string(r) != "null" {
		format, e := chatResponseFormat(r)
		if e != nil {
			return empty, false, e
		}
		out["text"] = object{"format": format}
	}
	for _, k := range []string{"seed", "presence_penalty", "frequency_penalty", "logprobs", "top_logprobs", "logit_bias"} {
		if _, ok := root[k]; ok {
			warn(k + " has no equivalent in this Responses adapter and was omitted")
		}
	}
	if r, ok := root["store"]; ok {
		var store bool
		if json.Unmarshal(r, &store) != nil {
			return empty, false, fmt.Errorf("store must be boolean")
		}
		if store {
			warn("store=true is omitted; this gateway uses stateless Responses requests")
		}
	}
	prepared.Warnings = append(prepared.Warnings, warnings...)
	prepared.Body, err = json.Marshal(out)
	return prepared, includeUsage, err
}

func chatContent(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []any{}, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return []any{}, nil
		}
		return []any{object{"type": "text", "text": s}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("content must be a string or content array")
	}
	result := []any{}
	for _, r := range parts {
		p, e := rawObject(r)
		if e != nil {
			return nil, e
		}
		typ, e := text(p["type"])
		if e != nil {
			return nil, e
		}
		switch typ {
		case "refusal":
			if e = only(p, "type", "refusal"); e != nil {
				return nil, e
			}
			s, e := text(p["refusal"])
			if e != nil {
				return nil, e
			}
			result = append(result, object{"type": "text", "text": s})
		case "text":
			if e = only(p, "type", "text"); e != nil {
				return nil, e
			}
			s, e := text(p["text"])
			if e != nil {
				return nil, e
			}
			result = append(result, object{"type": "text", "text": s})
		case "image_url":
			if e = only(p, "type", "image_url"); e != nil {
				return nil, e
			}
			var image struct {
				URL    string `json:"url"`
				Detail string `json:"detail"`
			}
			if json.Unmarshal(p["image_url"], &image) != nil || image.URL == "" {
				return nil, fmt.Errorf("image_url requires url")
			}
			if image.Detail != "" && image.Detail != "auto" && image.Detail != "high" && image.Detail != "low" {
				return nil, fmt.Errorf("image_url.detail must be auto, high or low")
			}
			source := object{"type": "url", "url": image.URL}
			if strings.HasPrefix(image.URL, "data:") {
				media, data, found := strings.Cut(strings.TrimPrefix(image.URL, "data:"), ";base64,")
				if !found {
					return nil, fmt.Errorf("invalid image data URL")
				}
				if _, e = base64.StdEncoding.DecodeString(data); e != nil {
					return nil, e
				}
				source = object{"type": "base64", "media_type": media, "data": data}
			}
			block := object{"type": "image", "source": source}
			if image.Detail != "" {
				block["detail"] = image.Detail
			}
			result = append(result, block)
		default:
			return nil, fmt.Errorf("unsupported Chat content type %q", typ)
		}
	}
	return result, nil
}

func chatResponseFormat(raw []byte) (object, error) {
	f, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	if err = only(f, "type", "json_schema"); err != nil {
		return nil, err
	}
	typ, err := text(f["type"])
	if err != nil {
		return nil, err
	}
	switch typ {
	case "text", "json_object":
		return object{"type": typ}, nil
	case "json_schema":
		s, e := rawObject(f["json_schema"])
		if e != nil {
			return nil, e
		}
		if e = only(s, "name", "description", "schema", "strict"); e != nil {
			return nil, e
		}
		name, e := text(s["name"])
		if e != nil || name == "" {
			return nil, fmt.Errorf("response_format json_schema requires name")
		}
		schema, e := rawObject(s["schema"])
		if e != nil {
			return nil, e
		}
		format := object{"type": "json_schema", "name": name, "schema": schema}
		if strict, ok := s["strict"]; ok {
			var enabled bool
			if json.Unmarshal(strict, &enabled) != nil {
				return nil, fmt.Errorf("json_schema.strict must be boolean")
			}
			format["strict"] = enabled
			if enabled {
				if e = validateStrictSchema(schema); e != nil {
					return nil, e
				}
			}
		}
		if description, ok := s["description"]; ok {
			format["description"] = description
		}
		return format, nil
	default:
		return nil, fmt.Errorf("unsupported response_format %q", typ)
	}
}
