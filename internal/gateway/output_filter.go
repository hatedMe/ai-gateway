package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"ai-gateway-go/internal/translate"
)

func restoreName(block map[string]any, names map[string]string) {
	if name, ok := block["name"].(string); ok {
		if original, found := names[name]; found {
			block["name"] = original
		}
	}
}

func firstStop(text string, stops []string) (int, string) {
	at, stop := -1, ""
	for _, candidate := range stops {
		if candidate == "" {
			continue
		}
		if i := strings.Index(text, candidate); i >= 0 && (at < 0 || i < at) {
			at, stop = i, candidate
		}
	}
	return at, stop
}

func transformResponse(raw []byte, prepared translate.PreparedRequest) ([]byte, error) {
	var message map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&message); err != nil {
		return nil, err
	}
	content, _ := message["content"].([]any)
	for _, value := range content {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		restoreName(block, prepared.ToolNames)
		if prepared.OmitThinking && block["type"] == "thinking" {
			signature, _ := block["signature"].(string)
			hidden, err := translate.OmitThinkingSignature(signature)
			if err != nil {
				return nil, err
			}
			block["thinking"] = ""
			block["signature"] = hidden
		}
	}
	for start := 0; start < len(content); {
		end := start
		var segment strings.Builder
		for end < len(content) {
			b, ok := content[end].(map[string]any)
			if !ok || b["type"] != "text" {
				break
			}
			s, _ := b["text"].(string)
			segment.WriteString(s)
			end++
		}
		if end == start {
			start++
			continue
		}
		if at, stop := firstStop(segment.String(), prepared.StopSequences); at >= 0 {
			for i := start; i < end; i++ {
				b := content[i].(map[string]any)
				s, _ := b["text"].(string)
				if at <= len(s) {
					b["text"] = s[:at]
					message["content"] = content[:i+1]
					break
				}
				at -= len(s)
			}
			message["stop_reason"] = "stop_sequence"
			message["stop_sequence"] = stop
			break
		}
		start = end
	}
	return json.Marshal(message)
}

type outputFilter struct {
	prepared  translate.PreparedRequest
	emit      func(string, any) error
	pending   string
	stop      string
	textBlock bool
	queue     []filteredEvent
	open      any
}

type filteredEvent struct {
	name   string
	event  map[string]any
	text   string
	isText bool
}

func newOutputFilter(prepared translate.PreparedRequest, emit func(string, any) error) *outputFilter {
	return &outputFilter{prepared: prepared, emit: emit}
}

func (f *outputFilter) send(name string, event map[string]any) error {
	if name == "content_block_start" {
		f.open = event["index"]
	}
	if name == "content_block_stop" {
		f.open = nil
	}
	return f.emit(name, event)
}

// drain releases only text proven not to be a stop prefix. Block boundaries
// remain queued with their text, allowing stops to span adjacent text blocks.
func (f *outputFilter) drain(n int) error {
	for len(f.queue) > 0 {
		e := &f.queue[0]
		if e.isText {
			if n == 0 && e.text != "" {
				break
			}
			take := len(e.text)
			if take > n {
				take = n
			}
			if take > 0 {
				delta := map[string]any{"type": "text_delta", "text": e.text[:take]}
				event := map[string]any{"type": "content_block_delta", "index": e.event["index"], "delta": delta}
				if err := f.send(e.name, event); err != nil {
					return err
				}
			}
			e.text = e.text[take:]
			n -= take
			if e.text != "" {
				break
			}
		} else if err := f.send(e.name, e.event); err != nil {
			return err
		}
		f.queue = f.queue[1:]
	}
	return nil
}

func (f *outputFilter) Emit(name string, payload any) error {
	// Normalize named map types emitted by the translator without unsafe assertions.
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var event map[string]any
	if err = json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if f.prepared.OmitThinking {
		if name == "content_block_start" {
			block, _ := event["content_block"].(map[string]any)
			if block["type"] == "thinking" {
				block["thinking"] = ""
				if signature, ok := block["signature"].(string); ok && signature != "" {
					hidden, err := translate.OmitThinkingSignature(signature)
					if err != nil {
						return err
					}
					block["signature"] = hidden
				}
			}
		}
		if name == "content_block_delta" {
			delta, _ := event["delta"].(map[string]any)
			if delta["type"] == "thinking_delta" {
				return nil
			}
			if delta["type"] == "signature_delta" {
				signature, _ := delta["signature"].(string)
				hidden, err := translate.OmitThinkingSignature(signature)
				if err != nil {
					return err
				}
				delta["signature"] = hidden
			}
		}
	}
	if name == "message_delta" && f.stop != "" {
		delta, _ := event["delta"].(map[string]any)
		if delta != nil {
			delta["stop_reason"] = "stop_sequence"
			delta["stop_sequence"] = f.stop
		}
	}
	if f.stop != "" && strings.HasPrefix(name, "content_block_") {
		return nil
	}
	if len(f.prepared.StopSequences) == 0 {
		if name == "content_block_start" {
			block, _ := event["content_block"].(map[string]any)
			restoreName(block, f.prepared.ToolNames)
		}
		return f.send(name, event)
	}
	switch name {
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		restoreName(block, f.prepared.ToolNames)
		f.textBlock = block["type"] == "text"
		if !f.textBlock {
			if err := f.drain(len(f.pending)); err != nil {
				return err
			}
			f.pending = ""
		} else {
			f.queue = append(f.queue, filteredEvent{name: name, event: event})
			return f.drain(0)
		}
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			value, _ := delta["text"].(string)
			f.pending += value
			f.queue = append(f.queue, filteredEvent{name: name, event: event, text: value, isText: true})
			if at, stop := firstStop(f.pending, f.prepared.StopSequences); at >= 0 {
				if err := f.drain(at); err != nil {
					return err
				}
				f.pending = ""
				f.stop = stop
				f.queue = nil
				if f.open != nil {
					return f.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": f.open})
				}
				return nil
			}
			keep := 0
			for _, stop := range f.prepared.StopSequences {
				for n := 1; n < len(stop) && n <= len(f.pending); n++ {
					if strings.HasSuffix(f.pending, stop[:n]) && n > keep {
						keep = n
					}
				}
			}
			n := len(f.pending) - keep
			f.pending = f.pending[len(f.pending)-keep:]
			return f.drain(n)
		}
		if f.textBlock {
			f.queue = append(f.queue, filteredEvent{name: name, event: event})
			return f.drain(0)
		}
	case "content_block_stop":
		if f.textBlock {
			f.queue = append(f.queue, filteredEvent{name: name, event: event})
			f.textBlock = false
			return f.drain(0)
		}
	default:
		if err := f.drain(len(f.pending)); err != nil {
			return err
		}
		f.pending = ""
	}
	return f.send(name, event)
}
