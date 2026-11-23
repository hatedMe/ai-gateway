package translate

import (
	"encoding/json"
	"fmt"
)

func DecodeUpstreamResponse(protocol Protocol, raw []byte) ([]byte, error) {
	if protocol == ResponsesProtocol {
		return raw, nil
	}
	var root object
	if err := decodeNumbers(raw, &root); err != nil || root == nil {
		return nil, fmt.Errorf("invalid upstream JSON")
	}
	id, _ := root["id"].(string)
	if id == "" {
		return nil, fmt.Errorf("missing upstream response id")
	}
	created, err := responseCreationTime(root, "response")
	if err != nil {
		return nil, err
	}
	result := object{"created_at": created, "id": id, "object": "response", "model": root["model"], "status": "completed"}
	output := []object{}
	usage := object{"input_tokens": 0, "output_tokens": 0}
	finish := ""
	if protocol == ChatProtocol {
		choices, ok := root["choices"].([]any)
		if !ok || len(choices) != 1 {
			return nil, fmt.Errorf("expected one upstream choice")
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid upstream choice")
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing upstream message")
		}
		if err := validateChatContent(msg, "response"); err != nil {
			return nil, err
		}
		if text, ok := msg["reasoning_content"].(string); ok && text != "" {
			output = append(output, object{"type": "reasoning", "summary": []object{{"type": "summary_text", "text": text}}})
		}
		if s, ok := msg["content"].(string); ok && s != "" {
			output = append(output, object{"type": "message", "role": "assistant", "content": []object{{"type": "output_text", "text": s}}})
		}
		if s, ok := msg["refusal"].(string); ok && s != "" {
			output = append(output, object{"type": "message", "role": "assistant", "content": []object{{"type": "refusal", "refusal": s}}})
		}
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, v := range calls {
				call, ok := v.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid tool call")
				}
				fn, ok := call["function"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("missing function")
				}
				output = append(output, object{"type": "function_call", "call_id": call["id"], "name": fn["name"], "arguments": fn["arguments"]})
			}
		}
		finish, _ = choice["finish_reason"].(string)
		if u, ok := root["usage"].(map[string]any); ok {
			var state usageMerger
			var err error
			usage, err = state.merge(protocol, u)
			if err != nil {
				return nil, err
			}
		}

	} else if protocol == AnthropicProtocol {
		blocks, ok := root["content"].([]any)
		if !ok {
			return nil, fmt.Errorf("missing upstream content")
		}
		for _, v := range blocks {
			b, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid content block")
			}
			switch b["type"] {
			case "thinking":
				text, ok := b["thinking"].(string)
				if !ok {
					return nil, &ConversionError{Code: "unsupported_capability", Stage: "response", Field: "content.thinking", Description: "invalid readable reasoning summary"}
				}
				output = append(output, object{"type": "reasoning", "summary": []object{{"type": "summary_text", "text": text}}})
			case "text":
				output = append(output, object{"type": "message", "role": "assistant", "content": []object{{"type": "output_text", "text": b["text"]}}})
			case "tool_use":
				args, err := json.Marshal(b["input"])
				if err != nil {
					return nil, err
				}
				output = append(output, object{"type": "function_call", "call_id": b["id"], "name": b["name"], "arguments": string(args)})
			default:
				return nil, fmt.Errorf("unsupported cross-protocol upstream content type %v", b["type"])
			}
		}
		finish, _ = root["stop_reason"].(string)
		if u, ok := root["usage"].(map[string]any); ok {
			var state usageMerger
			var err error
			usage, err = state.merge(protocol, u)
			if err != nil {
				return nil, err
			}
		}

	} else {
		return nil, fmt.Errorf("unsupported upstream protocol")
	}
	if finish == "refusal" {
		result["stop_reason"] = "refusal"
	}
	switch finish {
	case "stop", "tool_calls", "end_turn", "stop_sequence", "tool_use", "refusal":
	case "length", "max_tokens", "model_context_window_exceeded":
		result["status"] = "incomplete"
		result["incomplete_details"] = object{"reason": "max_output_tokens"}
	case "content_filter":
		result["status"] = "incomplete"
		result["incomplete_details"] = object{"reason": "content_filter"}
	default:
		return nil, fmt.Errorf("missing or unsupported upstream stop reason")
	}
	if err := validateOutputTools(output, "response"); err != nil {
		return nil, err
	}
	for index, item := range output {
		responseItemFields(item, id, index, result["status"].(string))
	}
	result["output"] = output
	result["usage"] = usage
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	// Validate identities, tool argument objects and usage with the existing parser.
	var response upstreamResponse
	_ = json.Unmarshal(encoded, &response)
	if _, err = response.messageFor("", true); err != nil {
		return nil, err
	}
	return encoded, nil
}
