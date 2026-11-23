package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Protocol string

const (
	ResponsesProtocol Protocol = "openai-responses"
	ChatProtocol      Protocol = "openai-compatible"
	AnthropicProtocol Protocol = "anthropic"
)

// EncodeUpstreamRequest encodes the supported, stateless Responses subset.
// Native requests bypass this function so their provider fields remain intact.
func EncodeUpstreamRequest(protocol Protocol, raw []byte) ([]byte, error) {
	if protocol == ResponsesProtocol {
		return raw, nil
	}
	if protocol != ChatProtocol && protocol != AnthropicProtocol {
		return nil, fmt.Errorf("unsupported upstream protocol")
	}
	root, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	if err = only(root, "model", "input", "instructions", "max_output_tokens", "stream", "store", "temperature", "top_p", "tools", "tool_choice", "parallel_tool_calls"); err != nil {
		return nil, err
	}
	if v, ok := root["store"]; ok && string(v) != "false" {
		return nil, fmt.Errorf("store is unsupported across upstream protocols")
	}
	var model string
	if json.Unmarshal(root["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	out := object{"model": model}
	for _, key := range []string{"stream", "temperature", "top_p"} {
		if v, ok := root[key]; ok {
			out[key] = v
		}
	}
	limit := json.RawMessage("4096")
	if v, ok := root["max_output_tokens"]; ok {
		limit = v
	}
	var maxTokens int
	if json.Unmarshal(limit, &maxTokens) != nil || maxTokens <= 0 {
		return nil, fmt.Errorf("max_output_tokens must be positive")
	}
	if protocol == ChatProtocol {
		out["max_completion_tokens"] = maxTokens
	} else {
		out["max_tokens"] = maxTokens
	}
	messages := []object{}
	appendText := func(role, text string) {
		if protocol == AnthropicProtocol && (role == "system" || role == "developer") {
			// Instructions must stay before conversational history.
			prior, _ := out["system"].(string)
			if prior != "" {
				prior += "\n\n"
			}
			out["system"] = prior + text
		} else {
			if protocol == ChatProtocol && role == "assistant" && len(messages) > 0 && messages[len(messages)-1]["role"] == "assistant" {
				last := messages[len(messages)-1]
				prior, _ := last["content"].(string)
				if prior != "" {
					prior += "\n\n"
				}
				last["content"] = prior + text
				return
			}
			messages = append(messages, object{"role": role, "content": text})
		}
	}
	if v, ok := root["instructions"]; ok {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil, fmt.Errorf("instructions must be text")
		}
		appendText("system", s)
	}
	var input []json.RawMessage
	if v, ok := root["input"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			input = []json.RawMessage{json.RawMessage(`{"role":"user","content":` + string(v) + `}`)}
		} else if json.Unmarshal(v, &input) != nil {
			return nil, fmt.Errorf("input must be text or an array")
		}
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("input must not be empty")
	}
	for _, rawItem := range input {
		item, err := rawObject(rawItem)
		if err != nil {
			return nil, err
		}
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		switch typ {
		case "", "message":
			if err = only(item, "type", "role", "content", "id", "status"); err != nil {
				return nil, err
			}
			var role string
			_ = json.Unmarshal(item["role"], &role)
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return nil, fmt.Errorf("unsupported message role")
			}
			if protocol == AnthropicProtocol && (role == "system" || role == "developer") && len(messages) > 0 {
				return nil, fmt.Errorf("instruction roles within history cannot be moved to Anthropic system")
			}
			s, err := upstreamText(item["content"])
			if err != nil {
				return nil, err
			}
			appendText(role, s)
		case "function_call":
			if err = only(item, "type", "id", "call_id", "name", "arguments", "status"); err != nil {
				return nil, err
			}
			var id, name, args string
			_ = json.Unmarshal(item["call_id"], &id)
			_ = json.Unmarshal(item["name"], &name)
			_ = json.Unmarshal(item["arguments"], &args)
			if id == "" || name == "" {
				return nil, fmt.Errorf("function call requires call_id and name")
			}
			var obj object
			if decodeNumbers([]byte(args), &obj) != nil || obj == nil {
				return nil, fmt.Errorf("function arguments must be a JSON object")
			}
			if protocol == ChatProtocol {
				call := object{"id": id, "type": "function", "function": object{"name": name, "arguments": args}}
				if len(messages) > 0 && messages[len(messages)-1]["role"] == "assistant" {
					m := messages[len(messages)-1]
					calls, _ := m["tool_calls"].([]object)
					m["tool_calls"] = append(calls, call)
				} else {
					messages = append(messages, object{"role": "assistant", "content": nil, "tool_calls": []object{call}})
				}
			} else {
				messages = append(messages, object{"role": "assistant", "content": []object{{"type": "tool_use", "id": id, "name": name, "input": obj}}})
			}
		case "function_call_output":
			if err = only(item, "type", "id", "call_id", "output", "status"); err != nil {
				return nil, err
			}
			var id string
			_ = json.Unmarshal(item["call_id"], &id)
			if id == "" {
				return nil, fmt.Errorf("tool result requires call_id")
			}
			s, err := upstreamText(item["output"])
			if err != nil {
				return nil, err
			}
			if protocol == ChatProtocol {
				messages = append(messages, object{"role": "tool", "tool_call_id": id, "content": s})
			} else {
				messages = append(messages, object{"role": "user", "content": []object{{"type": "tool_result", "tool_use_id": id, "content": s}}})
			}
		default:
			return nil, fmt.Errorf("unsupported cross-protocol input type %q", typ)
		}
	}
	if protocol == AnthropicProtocol {
		messages = mergeAnthropicMessages(messages)
	}
	out["messages"] = messages
	var tools []json.RawMessage
	if v, ok := root["tools"]; ok {
		if json.Unmarshal(v, &tools) != nil {
			return nil, fmt.Errorf("tools must be an array")
		}
	}
	converted := []object{}
	for _, rawTool := range tools {
		tool, err := rawObject(rawTool)
		if err != nil {
			return nil, err
		}
		if err = only(tool, "type", "name", "description", "parameters", "strict"); err != nil {
			return nil, err
		}
		if string(tool["type"]) != `"function"` {
			return nil, fmt.Errorf("only function tools support cross-protocol conversion")
		}
		fn := object{}
		for _, k := range []string{"name", "description", "parameters", "strict"} {
			if v, ok := tool[k]; ok {
				fn[k] = v
			}
		}
		if protocol == ChatProtocol {
			converted = append(converted, object{"type": "function", "function": fn})
		} else {
			if v, ok := tool["strict"]; ok && string(v) != "false" {
				return nil, fmt.Errorf("strict tools are unsupported for cross-protocol Anthropic requests")
			}
			fn["input_schema"] = fn["parameters"]
			delete(fn, "parameters")
			delete(fn, "strict")
			converted = append(converted, fn)
		}
	}
	if len(converted) > 0 {
		out["tools"] = converted
	}
	if v, ok := root["tool_choice"]; ok {
		var choice string
		if json.Unmarshal(v, &choice) == nil {
			if protocol == ChatProtocol {
				out["tool_choice"] = choice
			} else {
				switch choice {
				case "auto", "none":
					out["tool_choice"] = object{"type": choice}
				case "required":
					out["tool_choice"] = object{"type": "any"}
				default:
					return nil, fmt.Errorf("unsupported tool_choice")
				}
			}
		} else {
			var chosen struct{ Type, Name string }
			if json.Unmarshal(v, &chosen) != nil || chosen.Type != "function" || chosen.Name == "" {
				return nil, fmt.Errorf("unsupported tool_choice")
			}
			if protocol == ChatProtocol {
				out["tool_choice"] = object{"type": "function", "function": object{"name": chosen.Name}}
			} else {
				out["tool_choice"] = object{"type": "tool", "name": chosen.Name}
			}
		}
	}
	if v, ok := root["parallel_tool_calls"]; ok {
		var parallel bool
		if json.Unmarshal(v, &parallel) != nil {
			return nil, fmt.Errorf("parallel_tool_calls must be boolean")
		}
		if protocol == ChatProtocol {
			out["parallel_tool_calls"] = parallel
		} else if len(converted) > 0 {
			tc, _ := out["tool_choice"].(object)
			if tc == nil {
				tc = object{"type": "auto"}
			}
			tc["disable_parallel_tool_use"] = !parallel
			out["tool_choice"] = tc
		}
	}
	if protocol == ChatProtocol && string(root["stream"]) == "true" {
		out["stream_options"] = object{"include_usage": true}
	}
	return json.Marshal(out)
}

func upstreamText(raw []byte) (string, error) {
	if string(raw) == "null" {
		return "", fmt.Errorf("content must not be null")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return "", fmt.Errorf("cross-protocol content must contain text")
	}
	var b strings.Builder
	for _, rawPart := range parts {
		part, err := rawObject(rawPart)
		if err != nil {
			return "", err
		}
		if err = only(part, "type", "text"); err != nil {
			return "", err
		}
		var typ, value string
		if json.Unmarshal(part["type"], &typ) != nil || (typ != "input_text" && typ != "output_text" && typ != "text") {
			return "", fmt.Errorf("unsupported cross-protocol content type")
		}
		if json.Unmarshal(part["text"], &value) != nil || string(part["text"]) == "null" {
			return "", fmt.Errorf("content text must be a string")
		}
		b.WriteString(value)
	}
	return b.String(), nil
}

func mergeAnthropicMessages(messages []object) []object {
	out := []object{}
	blocks := func(v any) []object {
		if s, ok := v.(string); ok {
			return []object{{"type": "text", "text": s}}
		}
		b, _ := v.([]object)
		return b
	}
	for _, m := range messages {
		if len(out) > 0 && out[len(out)-1]["role"] == m["role"] {
			last := out[len(out)-1]
			last["content"] = append(blocks(last["content"]), blocks(m["content"])...)
		} else {
			out = append(out, m)
		}
	}
	return out
}
