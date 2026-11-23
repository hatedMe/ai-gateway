package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeUpstreamRequest(t *testing.T) {
	raw := []byte(`{"model":"real","max_output_tokens":64,"stream":false,"store":false,"input":[{"role":"developer","content":"be helpful"},{"role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},{"type":"function_call_output","call_id":"call_1","output":"found"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":false}],"tool_choice":"auto"}`)
	for _, p := range []Protocol{ChatProtocol, AnthropicProtocol} {
		got, err := EncodeUpstreamRequest(p, raw)
		if err != nil {
			t.Fatal(p, err)
		}
		var obj object
		_ = json.Unmarshal(got, &obj)
		if obj["model"] != "real" || obj["input"] != nil || !strings.Contains(string(got), "call_1") || !strings.Contains(string(got), "found") {
			t.Fatal(string(got))
		}
	}
	for _, extra := range []string{`"previous_response_id":"r"`, `"background":true`, `"include":["reasoning.encrypted_content"]`, `"unknown":true`} {
		raw := []byte(`{"model":"real","input":"hello",` + extra + `}`)
		if _, err := EncodeUpstreamRequest(ChatProtocol, raw); err == nil {
			t.Fatal("accepted unsupported field", extra)
		}
	}
}

func TestEncodeUpstreamAssistantTurn(t *testing.T) {
	raw := []byte(`{"model":"real","input":[{"type":"function_call","call_id":"a","name":"lookup","arguments":"{}"},{"role":"assistant","content":"also"},{"type":"function_call","call_id":"b","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call_output","call_id":"b","output":"two"}]}`)
	encoded, err := EncodeUpstreamRequest(ChatProtocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role      string
			Content   any
			ToolCalls []any `json:"tool_calls"`
		}
	}
	_ = json.Unmarshal(encoded, &req)
	if len(req.Messages) != 3 || req.Messages[0].Role != "assistant" || len(req.Messages[0].ToolCalls) != 2 || req.Messages[0].Content != "also" || req.Messages[1].Role != "tool" {
		t.Fatal(string(encoded))
	}
}

func TestDecodeUpstreamResponse(t *testing.T) {
	cases := map[Protocol]string{
		ChatProtocol:      `{"id":"chat1","model":"real","choices":[{"index":0,"message":{"role":"assistant","content":"hi","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`,
		AnthropicProtocol: `{"id":"msg1","type":"message","model":"real","role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"call_1","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":8,"output_tokens":3}}`,
	}
	for p, raw := range cases {
		got, err := DecodeUpstreamResponse(p, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		var result upstreamResponse
		_ = json.Unmarshal(got, &result)
		if result.Status != "completed" || result.Usage.InputTokens != 8 || len(result.Output) != 2 || result.Output[1].CallID != "call_1" {
			t.Fatal(string(got))
		}
		if _, err := ConvertResponse(got, "public"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DecodeUpstreamResponse(ChatProtocol, []byte(`{"id":"bad","choices":[]}`)); err == nil {
		t.Fatal("accepted missing choice")
	}
}
