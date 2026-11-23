package translate

import (
	"encoding/json"
	"testing"
)

func TestAnthropicNullableUsageTail(t *testing.T) {
	var state usageMerger
	var first, tail map[string]any
	_ = decodeNumbers([]byte(`{"input_tokens":5,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":1}`), &first)
	_ = decodeNumbers([]byte(`{"input_tokens":null,"cache_read_input_tokens":null,"cache_creation_input_tokens":null,"output_tokens":4}`), &tail)
	if _, err := state.merge(AnthropicProtocol, first); err != nil {
		t.Fatal(err)
	}
	out, err := state.merge(AnthropicProtocol, tail)
	if err != nil {
		t.Fatal(err)
	}
	if out["input_tokens"] != int64(10) || out["output_tokens"] != int64(4) {
		raw, _ := json.Marshal(out)
		t.Fatal(string(raw))
	}
}

func TestUsageMergeValidation(t *testing.T) {
	for _, usage := range []string{`{"prompt_tokens":-1}`, `{"completion_tokens":9223372036854775808}`, `{"prompt_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}`, `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":1}`, `{"prompt_tokens":9223372036854775807,"completion_tokens":1}`} {
		frames := chatFrames(streamChunk(`{"content":"ok"}`), chatTerminalFixture, `{"id":"c","choices":[],"usage":`+usage+`}`, "[DONE]")
		_, err := collectStream(t, frames)
		if err == nil {
			t.Errorf("accepted %s", usage)
		}
	}
	raw := `{"id":"a","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":9223372036854775807,"cache_read_input_tokens":1}}`
	if _, err := DecodeUpstreamResponse(AnthropicProtocol, []byte(raw)); err == nil {
		t.Fatal("accepted overflowing total")
	}
}

func TestStreamUsageTail(t *testing.T) {
	events, err := collectStream(t, chatFrames(streamChunk(`{"content":"ok"}`), chatTerminalFixture, `{"id":"c","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, "[DONE]"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e["type"] == "response.completed" {
			count++
			u := e["response"].(map[string]any)["usage"].(map[string]any)
			if u["input_tokens"] != float64(7) {
				t.Fatal(u)
			}
		}
	}
	if count != 1 {
		t.Fatal(events)
	}
}

func TestStreamTerminalContract(t *testing.T) {
	for _, tail := range []string{"", "data: [DONE]\n\n"} {
		events, err := collectStream(t, chatFrames(streamChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{"}}]}`), chatTerminalFixture)+tail)
		if err == nil {
			t.Fatal("accepted truncated arguments")
		}
		for _, e := range events {
			if e["type"] == "response.completed" || e["type"] == "response.function_call_arguments.done" {
				t.Fatal(e)
			}
		}
	}

}
