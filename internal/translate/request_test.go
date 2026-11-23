package translate

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRequestToolHistory(t *testing.T) {
	raw := []byte(`{"model":"claude-alias","system":[{"type":"text","text":"be helpful"},{"type":"text","text":"be concise"}],"max_tokens":100,"tools":[{"name":"weather","description":"weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"上海"}},{"type":"tool_use","id":"toolu_2","name":"weather","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"},{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"unavailable"}],"is_error":true},{"type":"text","text":"summarize"}]}]}`)
	b, err := ConvertRequest(raw, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	v := decode(t, b)
	if v["model"] != "gpt-test" || v["max_output_tokens"] != float64(100) || v["store"] != false {
		t.Fatal(v)
	}
	if v["parallel_tool_calls"] != false || v["tool_choice"] != "required" {
		t.Fatal(v)
	}
	input := v["input"].([]any)
	if len(input) != 8 {
		t.Fatalf("input sequence: %s", b)
	}
	if input[0].(map[string]any)["role"] != "developer" {
		t.Fatal(input)
	}
	call := input[3].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "toolu_1" {
		t.Fatal(call)
	}
	if decode(t, []byte(call["arguments"].(string)))["city"] != "上海" {
		t.Fatal(call)
	}
	result := input[6].(map[string]any)
	if result["type"] != "function_call_output" || result["output"] != "[tool_error] unavailable" {
		t.Fatal(result)
	}
	tools := v["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "function" {
		t.Fatal(tools)
	}
}

func TestRequestTextAndImage(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":64,"stream":true,"metadata":{"user_id":"u"},"temperature":0.2,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`)
	b, err := ConvertRequest(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	v := decode(t, b)
	if v["stream"] != true || v["model"] != "m" {
		t.Fatal(v)
	}
	input := v["input"].([]any)
	parts := input[0].(map[string]any)["content"].([]any)
	if len(parts) != 3 || parts[2].(map[string]any)["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatal(parts)
	}
}

func TestRequestRejectsUnsupportedAndInvalid(t *testing.T) {
	base := `"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]`
	cases := []string{
		`{}`, `null`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":64,"messages":[]}`,
		`{` + base + `,"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{` + base + `,"stop_sequences":[""]}`,
		`{` + base + `,"unknown":true}`,
		`{` + base + `,"tools":[{"type":"unknown_server_tool","name":"web_search"}]}`,
		`{` + base + `,"tool_choice":{"type":"tool","name":"missing"}}`,
		`{"model":"m","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":[]}]}]}`,
		`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"x"}]}]}`,
		`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"invalid"}}]}]}`,
		`{` + base + `} {}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := ConvertRequest([]byte(raw), ""); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}
