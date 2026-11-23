package translate

import (
	"encoding/json"
	"fmt"
	"time"
)

// ChatStream translates filtered Messages events into Chat chunks. A nil chunk
// means [DONE]; errors and disconnected upstreams never emit that sentinel.
type ChatStream struct {
	refusals         map[int]bool
	model, id        string
	created          int64
	includeUsage     bool
	emit             func(any) error
	toolIndices      map[int]int
	reasoningIndices map[int]int
	usage            chatSourceUsage
}

func NewChatStream(model string, includeUsage bool, emit func(any) error) *ChatStream {
	return &ChatStream{model: model, created: time.Now().Unix(), includeUsage: includeUsage, emit: emit, toolIndices: map[int]int{}, reasoningIndices: map[int]int{}, refusals: map[int]bool{}}
}

func (s *ChatStream) chunk(delta object, finish any) error {
	chunk := object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []object{{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}}}
	if s.includeUsage {
		chunk["usage"] = nil
	}
	return s.emit(chunk)
}

func (s *ChatStream) Emit(name string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var e struct {
		Index        int               `json:"index"`
		Message      chatSourceMessage `json:"message"`
		ContentBlock object            `json:"content_block"`
		Delta        object            `json:"delta"`
		Usage        chatSourceUsage   `json:"usage"`
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		return err
	}
	switch name {
	case "message_start":
		s.id = chatID(e.Message.ID)
		s.usage = e.Message.Usage
		return s.chunk(object{"role": "assistant", "content": ""}, nil)
	case "content_block_start":
		switch e.ContentBlock["type"] {
		case "tool_use":
			index := len(s.toolIndices)
			s.toolIndices[e.Index] = index
			return s.chunk(object{"tool_calls": []object{{"index": index, "id": e.ContentBlock["id"], "type": "function", "function": object{"name": e.ContentBlock["name"], "arguments": ""}}}}, nil)
		case "thinking":
			index := len(s.reasoningIndices)
			s.reasoningIndices[e.Index] = index
			return s.chunk(object{"reasoning_blocks": []object{{"index": index, "type": "thinking", "thinking": ""}}}, nil)
		case "text":
			s.refusals[e.Index] = e.ContentBlock["refusal"] == true
			if value, ok := e.ContentBlock["text"].(string); ok && value != "" {
				if s.refusals[e.Index] {
					return s.chunk(object{"refusal": value}, nil)
				}
				return s.chunk(object{"content": value}, nil)
			}
		default:
			encoded, _ := json.Marshal(e.ContentBlock)
			return s.chunk(object{"content": "\n[Upstream content] " + string(encoded)}, nil)
		}
	case "content_block_delta":
		switch e.Delta["type"] {
		case "text_delta":
			if s.refusals[e.Index] {
				return s.chunk(object{"refusal": e.Delta["text"]}, nil)
			}
			return s.chunk(object{"content": e.Delta["text"]}, nil)
		case "thinking_delta":
			index, ok := s.reasoningIndices[e.Index]
			if !ok {
				return fmt.Errorf("thinking delta references missing block")
			}
			return s.chunk(object{"reasoning_content": e.Delta["thinking"], "reasoning_blocks": []object{{"index": index, "thinking": e.Delta["thinking"]}}}, nil)
		case "signature_delta":
			index, ok := s.reasoningIndices[e.Index]
			if !ok {
				return fmt.Errorf("signature references missing block")
			}
			return s.chunk(object{"reasoning_blocks": []object{{"index": index, "signature": e.Delta["signature"]}}}, nil)
		case "input_json_delta":
			index, ok := s.toolIndices[e.Index]
			if !ok {
				return fmt.Errorf("tool delta references missing block")
			}
			return s.chunk(object{"tool_calls": []object{{"index": index, "function": object{"arguments": e.Delta["partial_json"]}}}}, nil)
		case "citations_delta":
			return s.chunk(object{"citations": []any{e.Delta["citation"]}}, nil)
		}
	case "message_delta":
		s.usage = e.Usage
		reason, _ := e.Delta["stop_reason"].(string)
		return s.chunk(object{}, chatFinish(reason))
	case "message_stop":
		if s.includeUsage {
			if err = s.emit(object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{}, "usage": s.usage.chat()}); err != nil {
				return err
			}
		}
		return s.emit(nil)
	case "error":
		var v object
		_ = json.Unmarshal(raw, &v)
		return s.emit(v)
	}
	return nil
}
