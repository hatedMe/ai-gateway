package translate

import (
	"encoding/json"
	"fmt"
	"io"
)

type normalizedStream struct {
	id, model, finish string
	output            []object
	keys              map[int]int
	closed            map[int]bool
	usage             object
	sequence          int
	bytes             int
	emit              func(string, any) error
}

func (s *normalizedStream) event(name string, payload object) error {
	payload["type"] = name
	payload["sequence_number"] = s.sequence
	s.sequence++
	return s.emit(name, payload)
}
func (s *normalizedStream) start(id, model string) error {
	if id == "" {
		return fmt.Errorf("missing upstream stream id")
	}
	if s.id != "" {
		if s.id != id {
			return fmt.Errorf("upstream stream id changed")
		}
		return nil
	}
	s.id = id
	s.model = model
	usage := object{}
	for k, v := range s.usage {
		usage[k] = v
	}
	if total, ok := usage["_total_input_tokens"]; ok {
		usage["input_tokens"] = total
		delete(usage, "_total_input_tokens")
	}
	return s.event("response.created", object{"response": object{"id": id, "model": model, "status": "in_progress", "output": []object{}, "usage": usage}})
}
func (s *normalizedStream) add(key int, item object) (int, error) {
	if s.id == "" {
		return 0, fmt.Errorf("content before stream start")
	}
	if _, ok := s.keys[key]; ok {
		return 0, fmt.Errorf("duplicate stream item")
	}
	index := len(s.output)
	s.keys[key] = index
	s.output = append(s.output, item)
	if err := s.event("response.output_item.added", object{"output_index": index, "item": item}); err != nil {
		return 0, err
	}
	if item["type"] == "message" {
		if err := s.event("response.content_part.added", object{"output_index": index, "content_index": 0, "part": object{"type": "output_text", "text": ""}}); err != nil {
			return 0, err
		}
	}
	return index, nil
}
func (s *normalizedStream) delta(key int, value string, tool bool) error {
	index, ok := s.keys[key]
	if !ok || s.closed[key] {
		return fmt.Errorf("delta references missing or closed item")
	}
	s.bytes += len(value)
	if s.bytes > 32<<20 {
		return fmt.Errorf("upstream stream content exceeds 32 MiB")
	}
	item := s.output[index]
	if tool {
		if item["type"] != "function_call" {
			return fmt.Errorf("tool delta in text item")
		}
		prior, _ := item["arguments"].(string)
		item["arguments"] = prior + value
		return s.event("response.function_call_arguments.delta", object{"output_index": index, "item_id": item["id"], "delta": value})
	}
	parts, ok := item["content"].([]object)
	if !ok {
		return fmt.Errorf("text delta in tool item")
	}
	prior, _ := parts[0]["text"].(string)
	parts[0]["text"] = prior + value
	return s.event("response.output_text.delta", object{"output_index": index, "content_index": 0, "item_id": item["id"], "delta": value})
}
func (s *normalizedStream) complete() error {
	if total, ok := s.usage["_total_input_tokens"]; ok {
		s.usage["input_tokens"] = total
		delete(s.usage, "_total_input_tokens")
	}
	if s.id == "" || s.finish == "" {
		return fmt.Errorf("upstream stream has no terminal reason")
	}
	status := "completed"
	response := object{"id": s.id, "object": "response", "model": s.model, "output": s.output, "usage": s.usage}
	switch s.finish {
	case "stop", "end_turn", "stop_sequence", "tool_use", "tool_calls":
	case "length", "max_tokens":
		status = "incomplete"
		response["incomplete_details"] = object{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		response["incomplete_details"] = object{"reason": "content_filter"}
	default:
		return fmt.Errorf("unsupported stream terminal reason")
	}
	response["status"] = status
	for _, item := range s.output {
		if item["type"] == "function_call" {
			args, _ := item["arguments"].(string)
			if _, err := rawObject([]byte(args)); err != nil {
				return fmt.Errorf("incomplete upstream function arguments")
			}
		}
	}
	raw, _ := json.Marshal(response)
	var parsed upstreamResponse
	_ = json.Unmarshal(raw, &parsed)
	if _, err := parsed.messageFor("", true); err != nil {
		return err
	}
	for index, item := range s.output {
		if item["type"] == "function_call" {
			if err := s.event("response.function_call_arguments.done", object{"output_index": index, "item_id": item["id"], "arguments": item["arguments"], "name": item["name"]}); err != nil {
				return err
			}
		} else {
			part := item["content"].([]object)[0]
			if err := s.event("response.output_text.done", object{"output_index": index, "content_index": 0, "item_id": item["id"], "text": part["text"]}); err != nil {
				return err
			}
			if err := s.event("response.content_part.done", object{"output_index": index, "content_index": 0, "item_id": item["id"], "part": part}); err != nil {
				return err
			}
		}
		item["status"] = "completed"
		if err := s.event("response.output_item.done", object{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	return s.event("response."+status, object{"response": response})
}

// DecodeUpstreamStream emits Responses events incrementally and only emits a
// terminal response after the source protocol's own terminal marker is valid.
func DecodeUpstreamStream(protocol Protocol, src io.Reader, emit func(string, any) error) error {
	if protocol != ChatProtocol && protocol != AnthropicProtocol {
		return fmt.Errorf("unsupported stream protocol")
	}
	s := normalizedStream{keys: map[int]int{}, closed: map[int]bool{}, usage: object{"input_tokens": 0, "output_tokens": 0}, emit: emit}
	return readSSE(src, func(raw []byte) (bool, error) {
		if protocol == ChatProtocol && string(raw) == "[DONE]" {
			return true, s.complete()
		}
		var root object
		if err := decodeNumbers(raw, &root); err != nil || root == nil {
			return false, fmt.Errorf("invalid upstream SSE JSON")
		}
		if root["error"] != nil || root["type"] == "error" {
			return false, fmt.Errorf("upstream stream reported an error")
		}
		if protocol == ChatProtocol {
			id, _ := root["id"].(string)
			model, _ := root["model"].(string)
			if err := s.start(id, model); err != nil {
				return false, err
			}
			if u, ok := root["usage"].(map[string]any); ok {
				if v, ok := u["prompt_tokens"]; ok {
					s.usage["input_tokens"] = v
				}
				if v, ok := u["completion_tokens"]; ok {
					s.usage["output_tokens"] = v
				}
				if v, ok := u["prompt_tokens_details"]; ok {
					s.usage["input_tokens_details"] = v
				}
			}
			choices, ok := root["choices"].([]any)
			if !ok {
				return false, fmt.Errorf("missing stream choices")
			}
			if len(choices) == 0 {
				return false, nil
			}
			if len(choices) != 1 {
				return false, fmt.Errorf("multiple stream choices unsupported")
			}
			choice, ok := choices[0].(map[string]any)
			if !ok {
				return false, fmt.Errorf("invalid stream choice")
			}
			if index, ok := choice["index"].(json.Number); !ok || index != "0" {
				return false, fmt.Errorf("unsupported choice index")
			}
			delta, ok := choice["delta"].(map[string]any)
			if !ok {
				return false, fmt.Errorf("missing stream delta")
			}
			if s.finish != "" && len(delta) > 0 {
				return false, fmt.Errorf("delta after finish reason")
			}
			if value, ok := delta["content"].(string); ok && value != "" {
				if _, exists := s.keys[-1]; !exists {
					if _, err := s.add(-1, object{"id": "msg_" + s.id, "type": "message", "role": "assistant", "content": []object{{"type": "output_text", "text": ""}}}); err != nil {
						return false, err
					}
				}
				if err := s.delta(-1, value, false); err != nil {
					return false, err
				}
			}
			if v, ok := delta["refusal"].(string); ok && v != "" {
				return false, fmt.Errorf("cross-protocol refusal stream unsupported")
			}
			if calls, ok := delta["tool_calls"].([]any); ok {
				for _, v := range calls {
					call, ok := v.(map[string]any)
					if !ok {
						return false, fmt.Errorf("invalid tool delta")
					}
					n, ok := call["index"].(json.Number)
					if !ok {
						return false, fmt.Errorf("missing tool index")
					}
					key, err := n.Int64()
					if err != nil || key < 0 || key > 1024 {
						return false, fmt.Errorf("invalid tool index")
					}
					fn, ok := call["function"].(map[string]any)
					if !ok {
						return false, fmt.Errorf("missing tool function")
					}
					if _, exists := s.keys[int(key)]; !exists {
						id, _ := call["id"].(string)
						name, _ := fn["name"].(string)
						if id == "" || name == "" {
							return false, fmt.Errorf("tool start requires id and name")
						}
						if _, err := s.add(int(key), object{"id": "fc_" + id, "type": "function_call", "call_id": id, "name": name, "arguments": ""}); err != nil {
							return false, err
						}
					} else {
						item := s.output[s.keys[int(key)]]
						if id, ok := call["id"].(string); ok && id != "" && id != item["call_id"] {
							return false, fmt.Errorf("tool id changed")
						}
						if name, ok := fn["name"].(string); ok && name != "" && name != item["name"] {
							return false, fmt.Errorf("tool name changed")
						}
					}
					if args, ok := fn["arguments"].(string); ok {
						if err := s.delta(int(key), args, true); err != nil {
							return false, err
						}
					}
				}
			}
			if reason, ok := choice["finish_reason"].(string); ok {
				if s.finish != "" {
					return false, fmt.Errorf("duplicate finish reason")
				}
				s.finish = reason
			}
			return false, nil
		}
		typ, _ := root["type"].(string)
		index := func() (int, error) {
			n, ok := root["index"].(json.Number)
			if !ok {
				return 0, fmt.Errorf("missing content index")
			}
			i, err := n.Int64()
			if err != nil || i < 0 || i > 1024 {
				return 0, fmt.Errorf("invalid content index")
			}
			return int(i), nil
		}
		switch typ {
		case "message_start":
			if s.id != "" {
				return false, fmt.Errorf("duplicate message_start")
			}
			message, ok := root["message"].(map[string]any)
			if !ok {
				return false, fmt.Errorf("missing message")
			}
			id, _ := message["id"].(string)
			model, _ := message["model"].(string)
			if u, ok := message["usage"].(map[string]any); ok {
				if err := s.anthropicUsage(u); err != nil {
					return false, err
				}
			}
			return false, s.start(id, model)
		case "content_block_start":
			if s.finish != "" {
				return false, fmt.Errorf("content after message_delta")
			}
			key, err := index()
			if err != nil {
				return false, err
			}
			if key != len(s.keys) {
				return false, fmt.Errorf("nonsequential content index")
			}
			block, ok := root["content_block"].(map[string]any)
			if !ok {
				return false, fmt.Errorf("missing content block")
			}
			switch block["type"] {
			case "text":
				if _, err = s.add(key, object{"id": fmt.Sprintf("msg_%s_%d", s.id, key), "type": "message", "role": "assistant", "content": []object{{"type": "output_text", "text": ""}}}); err != nil {
					return false, err
				}
				if text, ok := block["text"].(string); ok {
					return false, s.delta(key, text, false)
				}
			case "tool_use":
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if id == "" || name == "" {
					return false, fmt.Errorf("invalid tool start")
				}
				_, err = s.add(key, object{"id": "fc_" + id, "type": "function_call", "call_id": id, "name": name, "arguments": ""})
				if err == nil {
					input, ok := block["input"].(map[string]any)
					if !ok {
						return false, fmt.Errorf("tool input must be an object")
					}
					if len(input) > 0 {
						raw, _ := json.Marshal(input)
						err = s.delta(key, string(raw), true)
					}
				}
				return false, err
			default:
				return false, fmt.Errorf("unsupported upstream content block")
			}
		case "content_block_delta":
			if s.finish != "" {
				return false, fmt.Errorf("delta after message_delta")
			}
			key, err := index()
			if err != nil {
				return false, err
			}
			delta, ok := root["delta"].(map[string]any)
			if !ok {
				return false, fmt.Errorf("missing block delta")
			}
			switch delta["type"] {
			case "text_delta":
				value, ok := delta["text"].(string)
				if !ok {
					return false, fmt.Errorf("invalid text delta")
				}
				return false, s.delta(key, value, false)
			case "input_json_delta":
				value, ok := delta["partial_json"].(string)
				if !ok {
					return false, fmt.Errorf("invalid tool delta")
				}
				return false, s.delta(key, value, true)
			default:
				return false, fmt.Errorf("unsupported upstream delta")
			}
		case "content_block_stop":
			key, err := index()
			if err != nil {
				return false, err
			}
			i, ok := s.keys[key]
			if !ok || s.closed[key] {
				return false, fmt.Errorf("stop references missing or closed content")
			}
			item := s.output[i]
			if item["type"] == "function_call" && item["arguments"] == "" {
				if err = s.delta(key, "{}", true); err != nil {
					return false, err
				}
			}
			s.closed[key] = true
		case "message_delta":
			if s.id == "" || s.finish != "" || len(s.closed) != len(s.keys) {
				return false, fmt.Errorf("invalid message_delta lifecycle")
			}
			delta, ok := root["delta"].(map[string]any)
			if !ok {
				return false, fmt.Errorf("missing message delta")
			}
			s.finish, _ = delta["stop_reason"].(string)
			if u, ok := root["usage"].(map[string]any); ok {
				if err := s.anthropicUsage(u); err != nil {
					return false, err
				}
			}
		case "message_stop":
			return true, s.complete()
		case "ping":
		default:
			return false, fmt.Errorf("unsupported upstream SSE event")
		}
		return false, nil
	})
}

func (s *normalizedStream) anthropicUsage(u map[string]any) error {
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if v, ok := u[key]; ok {
			s.usage[key] = v
		}
	}
	total := int64(0)
	// Anthropic input_tokens excludes both cache categories. Keep its base value
	// separate when output-only usage updates arrive.
	for _, key := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if v, ok := s.usage[key]; ok {
			switch n := v.(type) {
			case json.Number:
				i, err := n.Int64()
				if err != nil || i < 0 {
					return fmt.Errorf("invalid token usage")
				}
				total += i
			case int:
				total += int64(n)
			}
		}
	}
	s.usage["input_tokens_details"] = object{"cached_tokens": s.usage["cache_read_input_tokens"]}
	// Store the base input count until completion; normalize it in complete.
	s.usage["_total_input_tokens"] = total
	return nil
}
