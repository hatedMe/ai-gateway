package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type normalizedStream struct {
	id, model, finish string
	output            []object
	keys              map[int]int
	closed            map[int]bool
	usage             object
	usageState        usageMerger
	createdAt         int64
	sequence          int
	bytes             int
	buffers           map[int]*strings.Builder
	summaryAsText     bool
	omitSummary       bool
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
	s.bytes += len(id) + len(model)
	if s.bytes > 32<<20 {
		return fmt.Errorf("upstream stream content exceeds 32 MiB")
	}
	if s.createdAt == 0 {
		s.createdAt, _ = responseCreationTime(object{}, "stream")
	}
	s.id = id
	s.model = model
	usage := object{}
	for k, v := range s.usage {
		usage[k] = v
	}
	return s.event("response.created", object{"response": object{"created_at": s.createdAt, "id": id, "model": model, "status": "in_progress", "output": []object{}, "usage": usage}})
}
func (s *normalizedStream) add(key int, item object) (int, error) {
	if s.id == "" {
		return 0, fmt.Errorf("content before stream start")
	}
	if _, ok := s.keys[key]; ok {
		return 0, fmt.Errorf("duplicate stream item")
	}
	if len(s.output) >= 32768 {
		return 0, fmt.Errorf("upstream stream item limit exceeded")
	}
	index := len(s.output)
	responseItemFields(item, s.id, index, "in_progress")
	s.keys[key] = index
	s.output = append(s.output, item)
	if s.buffers == nil {
		s.buffers = map[int]*strings.Builder{}
	}
	s.buffers[key] = &strings.Builder{}
	if err := s.event("response.output_item.added", object{"output_index": index, "item": item}); err != nil {
		return 0, err
	}
	if item["type"] == "reasoning" {
		if err := s.event("response.reasoning_summary_part.added", object{"output_index": index, "summary_index": 0, "item_id": item["id"], "part": object{"type": "summary_text", "text": ""}}); err != nil {
			return 0, err
		}
	}
	if item["type"] == "message" {
		if err := s.event("response.content_part.added", object{"output_index": index, "content_index": 0, "item_id": item["id"], "part": item["content"].([]object)[0]}); err != nil {
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
		s.buffers[key].WriteString(value)
		return s.event("response.function_call_arguments.delta", object{"output_index": index, "item_id": item["id"], "delta": value})
	}
	if item["type"] == "reasoning" {
		s.buffers[key].WriteString(value)
		return s.event("response.reasoning_summary_text.delta", object{"output_index": index, "summary_index": 0, "item_id": item["id"], "delta": value})
	}
	parts, ok := item["content"].([]object)
	if !ok {
		return fmt.Errorf("text delta in tool item")
	}
	s.buffers[key].WriteString(value)
	if parts[0]["type"] == "refusal" {
		return s.event("response.refusal.delta", object{"output_index": index, "content_index": 0, "item_id": item["id"], "delta": value})
	}
	return s.event("response.output_text.delta", object{"output_index": index, "content_index": 0, "item_id": item["id"], "delta": value})
}
func (s *normalizedStream) complete() error {
	for key, index := range s.keys {
		item := s.output[index]
		if item["type"] == "function_call" {
			item["arguments"] = s.buffers[key].String()
		} else if item["type"] == "reasoning" {
			item["summary"].([]object)[0]["text"] = s.buffers[key].String()
		} else {
			part := item["content"].([]object)[0]
			field := "text"
			if part["type"] == "refusal" {
				field = "refusal"
			}
			part[field] = s.buffers[key].String()
		}
	}
	if s.id == "" || s.finish == "" {
		return &ConversionError{Code: "incomplete_stream", Stage: "stream", Description: "upstream stream has no terminal reason"}
	}
	status := "completed"
	response := object{"created_at": s.createdAt, "id": s.id, "object": "response", "model": s.model, "output": s.output, "usage": s.usage}
	if s.finish == "refusal" {
		response["stop_reason"] = "refusal"
	}
	switch s.finish {
	case "stop", "end_turn", "stop_sequence", "tool_use", "tool_calls", "refusal":
	case "length", "max_tokens", "model_context_window_exceeded":
		status = "incomplete"
		response["incomplete_details"] = object{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		response["incomplete_details"] = object{"reason": "content_filter"}
	default:
		return fmt.Errorf("unsupported stream terminal reason")
	}
	response["status"] = status
	if err := validateOutputTools(s.output, "stream"); err != nil {
		return err
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
		} else if item["type"] == "reasoning" {
			part := item["summary"].([]object)[0]
			if err := s.event("response.reasoning_summary_text.done", object{"output_index": index, "summary_index": 0, "item_id": item["id"], "text": part["text"]}); err != nil {
				return err
			}
			if err := s.event("response.reasoning_summary_part.done", object{"output_index": index, "summary_index": 0, "item_id": item["id"], "part": part}); err != nil {
				return err
			}
		} else {
			part := item["content"].([]object)[0]
			event, field := "response.output_text.done", "text"
			if part["type"] == "refusal" {
				event, field = "response.refusal.done", "refusal"
			}
			if err := s.event(event, object{"output_index": index, "content_index": 0, "item_id": item["id"], field: part[field]}); err != nil {
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
	return decodeUpstreamStream(protocol, src, emit, false, false)
}
func decodeUpstreamStream(protocol Protocol, src io.Reader, emit func(string, any) error, summaryAsText, omitSummary bool) error {
	if protocol != ChatProtocol && protocol != AnthropicProtocol {
		return fmt.Errorf("unsupported stream protocol")
	}
	s := normalizedStream{keys: map[int]int{}, closed: map[int]bool{}, usage: object{"input_tokens": 0, "output_tokens": 0}, emit: emit, summaryAsText: summaryAsText, omitSummary: omitSummary}
	chat := chatSourceState{}
	return readSSE(src, func(raw []byte) (bool, error) {
		if protocol == ChatProtocol && string(raw) == "[DONE]" {
			if len(chat.pending) > 0 {
				return false, &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Description: "incomplete tool identity"}
			}
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
			if s.id == "" {
				var err error
				s.createdAt, err = responseCreationTime(root, "stream")
				if err != nil {
					return false, err
				}
			}
			if err := s.start(id, model); err != nil {
				return false, err
			}
			if u, ok := root["usage"].(map[string]any); ok {
				var err error
				s.usage, err = s.usageState.merge(ChatProtocol, u)
				if err != nil {
					return false, err
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
			if err := validateChatContent(delta, "stream"); err != nil {
				return false, err
			}
			if s.finish != "" && len(delta) > 0 {
				return false, fmt.Errorf("delta after finish reason")
			}
			for _, field := range []string{"reasoning_content", "content", "refusal"} {
				if value, ok := delta[field].(string); ok {
					kind := "text"
					if field == "reasoning_content" {
						kind = "reasoning"
					}
					if field == "refusal" {
						kind = "refusal"
					}
					if err := chat.content(&s, value, kind); err != nil {
						return false, err
					}
				}
			}
			if calls, ok := delta["tool_calls"].([]any); ok {
				for _, value := range calls {
					call, ok := value.(map[string]any)
					if !ok {
						return false, fmt.Errorf("invalid tool delta")
					}
					if err := chat.tool(&s, call); err != nil {
						return false, err
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
			case "thinking":
				if _, err = s.add(key, object{"id": fmt.Sprintf("rs_%s_%d", s.id, key), "type": "reasoning", "summary": []object{{"type": "summary_text", "text": ""}}}); err != nil {
					return false, err
				}
				if text, ok := block["thinking"].(string); ok {
					return false, s.delta(key, text, false)
				}
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
				s.bytes += len(id) + len(name)
				if s.bytes > 32<<20 {
					return false, fmt.Errorf("upstream stream content exceeds 32 MiB")
				}
				if id == "" || name == "" {
					return false, &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Field: "content_block", Description: "invalid tool start"}
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
			case "signature_delta":
				i, exists := s.keys[key]
				if !exists || s.closed[key] || s.output[i]["type"] != "reasoning" {
					return false, fmt.Errorf("signature references invalid reasoning item")
				}
				if _, ok := delta["signature"].(string); !ok {
					return false, fmt.Errorf("invalid reasoning signature")
				}
				return false, nil
			case "thinking_delta":
				value, ok := delta["thinking"].(string)
				if !ok {
					return false, fmt.Errorf("invalid thinking delta")
				}
				return false, s.delta(key, value, false)
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
			if item["type"] == "function_call" && s.buffers[key].Len() == 0 {
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
	var err error
	s.usage, err = s.usageState.merge(AnthropicProtocol, u)
	return err
}
