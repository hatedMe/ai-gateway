package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-gateway-go/internal/translate"
)

func TestOutputFilterStopsAcrossChunksAndRestoresNames(t *testing.T) {
	var events []map[string]any
	filter := newOutputFilter(translate.PreparedRequest{StopSequences: []string{"结束"}, ToolNames: map[string]string{"short": "original"}}, func(name string, v any) error {
		raw, _ := json.Marshal(v)
		var e map[string]any
		_ = json.Unmarshal(raw, &e)
		e["event"] = name
		events = append(events, e)
		return nil
	})
	for _, e := range []struct {
		name  string
		value map[string]any
	}{
		{"content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}},
		{"content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "你好结"}}},
		{"content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "束不应泄漏"}}},
		{"content_block_stop", map[string]any{"index": 0}},
		{"content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "name": "short"}}},
		{"content_block_stop", map[string]any{"index": 1}},
		{"message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}}},
		{"message_stop", map[string]any{}},
	} {
		if err := filter.Emit(e.name, e.value); err != nil {
			t.Fatal(err)
		}
	}
	var text strings.Builder
	stops := 0
	for _, e := range events {
		if e["event"] == "content_block_delta" {
			text.WriteString(e["delta"].(map[string]any)["text"].(string))
		}
		if e["event"] == "content_block_stop" {
			stops++
		}
		if e["event"] == "message_delta" {
			delta := e["delta"].(map[string]any)
			if delta["stop_reason"] != "stop_sequence" || delta["stop_sequence"] != "结束" {
				t.Fatal(delta)
			}
		}
	}
	if text.String() != "你好" || stops != 1 {
		t.Fatalf("%q stops=%d events=%v", text.String(), stops, events)
	}
	raw, err := transformResponse([]byte(`{"content":[{"type":"tool_use","name":"short"},{"type":"text","text":"ok结束tail"},{"type":"tool_use","name":"short"}],"stop_reason":"tool_use"}`), filter.prepared)
	if err != nil || !strings.Contains(string(raw), `"name":"original"`) || strings.Contains(string(raw), "tail") || strings.Count(string(raw), `"type":"tool_use"`) != 1 {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestOutputFilterFlushesUnmatchedPrefix(t *testing.T) {
	var text strings.Builder
	f := newOutputFilter(translate.PreparedRequest{StopSequences: []string{"STOP"}}, func(name string, p any) error {
		if name == "content_block_delta" {
			e := p.(map[string]any)
			text.WriteString(e["delta"].(map[string]any)["text"].(string))
		}
		return nil
	})
	_ = f.Emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text"}})
	_ = f.Emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "hello ST"}})
	_ = f.Emit("content_block_stop", map[string]any{"index": 0})
	_ = f.Emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}})
	if text.String() != "hello ST" {
		t.Fatal(text.String())
	}
}

func TestOutputFilterStopsAcrossTextBlocks(t *testing.T) {
	var text strings.Builder
	var reason string
	f := newOutputFilter(translate.PreparedRequest{StopSequences: []string{"STOP"}}, func(name string, p any) error {
		e := p.(map[string]any)
		if name == "content_block_delta" {
			text.WriteString(e["delta"].(map[string]any)["text"].(string))
		}
		if name == "message_delta" {
			reason = e["delta"].(map[string]any)["stop_reason"].(string)
		}
		return nil
	})
	for i, value := range []string{"hi ST", "OP tail"} {
		_ = f.Emit("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "text"}})
		_ = f.Emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "text_delta", "text": value}})
		_ = f.Emit("content_block_stop", map[string]any{"index": i})
	}
	_ = f.Emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}})
	if text.String() != "hi " || reason != "stop_sequence" {
		t.Fatalf("%q %s", text.String(), reason)
	}
	raw, err := transformResponse([]byte(`{"content":[{"type":"text","text":"hi ST"},{"type":"text","text":"OP tail"}]}`), f.prepared)
	if err != nil || !strings.Contains(string(raw), `"text":"hi "`) || strings.Contains(string(raw), "tail") {
		t.Fatalf("%s %v", raw, err)
	}
}
