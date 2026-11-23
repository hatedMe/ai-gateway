package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeUpstreamStream(t *testing.T) {
	cases := map[Protocol]string{
		ChatProtocol:      "data: {\"id\":\"chat1\",\"model\":\"real\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		AnthropicProtocol: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg1\",\"model\":\"real\",\"usage\":{\"input_tokens\":3}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for p, source := range cases {
		t.Run(string(p), func(t *testing.T) {
			var normalized strings.Builder
			err := DecodeUpstreamStream(p, strings.NewReader(source), func(name string, payload any) error {
				b, _ := json.Marshal(payload)
				fmt.Fprintf(&normalized, "event: %s\ndata: %s\n\n", name, b)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(normalized.String(), "response.output_text.delta") {
				t.Fatal("no incremental output")
			}
			var events []string
			for _, name := range []string{"response.output_text.done", "response.content_part.done", "response.output_item.done"} {
				if !strings.Contains(normalized.String(), name) {
					t.Error("missing standard completion event", name)
				}
			}
			if err = ConvertStream(strings.NewReader(normalized.String()), "public", func(name string, _ any) error { events = append(events, name); return nil }); err != nil {
				t.Fatal(err, normalized.String())
			}
			if events[len(events)-1] != "message_stop" {
				t.Fatal(events)
			}
			truncated := source[:strings.LastIndex(strings.TrimSpace(source), "data:")]
			if err = DecodeUpstreamStream(p, strings.NewReader(truncated), func(string, any) error { return nil }); err == nil {
				t.Fatal("accepted truncated stream")
			}
		})
	}
}

func TestDecodeUpstreamStreamCachedUsage(t *testing.T) {
	source := `data: {"type":"message_start","message":{"id":"msg1","model":"real","usage":{"input_tokens":3,"cache_read_input_tokens":10}}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

data: {"type":"message_stop"}

`
	var normalized strings.Builder
	err := DecodeUpstreamStream(AnthropicProtocol, strings.NewReader(source), func(name string, payload any) error {
		raw, _ := json.Marshal(payload)
		fmt.Fprintf(&normalized, "data: %s\n\n", raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = ConvertStream(strings.NewReader(normalized.String()), "public", func(name string, payload any) error {
		if name == "message_start" {
			raw, _ := json.Marshal(payload)
			if !strings.Contains(string(raw), `"input_tokens":3`) || !strings.Contains(string(raw), `"cache_read_input_tokens":10`) {
				t.Fatal(string(raw))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDecodeUpstreamStreamInitialToolInput(t *testing.T) {
	source := `data: {"type":"message_start","message":{"id":"msg1","model":"real"}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"one"}}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

data: {"type":"message_stop"}

`
	var arguments string
	err := DecodeUpstreamStream(AnthropicProtocol, strings.NewReader(source), func(name string, payload any) error {
		if name == "response.function_call_arguments.done" {
			raw, _ := json.Marshal(payload)
			var obj struct{ Arguments string }
			_ = json.Unmarshal(raw, &obj)
			arguments = obj.Arguments
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if arguments != `{"q":"one"}` {
		t.Fatal("initial tool input was lost", arguments)
	}
}

func TestDecodeUpstreamStreamRejectsPartialTool(t *testing.T) {
	source := `data: {"id":"chat1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{"}}]},"finish_reason":null}]}

data: {"id":"chat1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	terminal := false
	err := DecodeUpstreamStream(ChatProtocol, strings.NewReader(source), func(name string, _ any) error {
		terminal = terminal || name == "response.completed" || name == "response.function_call_arguments.done"
		return nil
	})
	if err == nil || terminal {
		t.Fatal("incomplete tool was treated as successful", err, terminal)
	}
}
