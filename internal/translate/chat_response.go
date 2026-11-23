package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type chatSourceMessage struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Content    []object        `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      chatSourceUsage `json:"usage"`
}

type chatSourceUsage struct {
	Input   int `json:"input_tokens"`
	Output  int `json:"output_tokens"`
	Read    int `json:"cache_read_input_tokens"`
	Created int `json:"cache_creation_input_tokens"`
}

func (u chatSourceUsage) chat() object {
	prompt := u.Input + u.Read + u.Created
	return object{"prompt_tokens": prompt, "completion_tokens": u.Output, "total_tokens": prompt + u.Output, "prompt_tokens_details": object{"cached_tokens": u.Read}}
}

func chatFinish(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

func chatID(id string) string { return "chatcmpl_" + strings.TrimPrefix(id, "msg_") }

// ChatResponse converts the already validated/filtered Messages representation.
func ChatResponse(raw []byte) ([]byte, error) {
	var source chatSourceMessage
	if err := decodeNumbers(raw, &source); err != nil {
		return nil, err
	}
	if source.ID == "" {
		return nil, fmt.Errorf("missing message id")
	}
	var text strings.Builder
	var refusal strings.Builder
	citations := []any{}
	summaries := []string{}
	reasoning := []object{}
	calls := []object{}
	for _, block := range source.Content {
		switch block["type"] {
		case "text":
			value, _ := block["text"].(string)
			if block["refusal"] == true {
				refusal.WriteString(value)
			} else {
				text.WriteString(value)
			}
			if refs, ok := block["citations"].([]any); ok {
				citations = append(citations, refs...)
			}
		case "thinking":
			value, _ := block["thinking"].(string)
			if value != "" {
				summaries = append(summaries, value)
			}
			reasoning = append(reasoning, block)
		case "tool_use":
			arguments, err := json.Marshal(block["input"])
			if err != nil {
				return nil, err
			}
			calls = append(calls, object{"id": block["id"], "type": "function", "function": object{"name": block["name"], "arguments": string(arguments)}})
		default:
			encoded, _ := json.Marshal(block)
			text.WriteString("\n[Upstream content] " + string(encoded))
		}
	}
	message := object{"role": "assistant", "content": nil, "refusal": nil}
	if refusal.Len() > 0 {
		message["refusal"] = refusal.String()
	}
	if len(citations) > 0 {
		message["citations"] = citations
	}
	if text.Len() > 0 {
		message["content"] = text.String()
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	if len(reasoning) > 0 {
		message["reasoning_content"] = strings.Join(summaries, "\n\n")
		message["reasoning_blocks"] = reasoning
	}
	choice := object{"index": 0, "message": message, "finish_reason": chatFinish(source.StopReason), "logprobs": nil}
	return json.Marshal(object{"id": chatID(source.ID), "object": "chat.completion", "created": time.Now().Unix(), "model": source.Model, "choices": []object{choice}, "usage": source.Usage.chat()})
}
