package translate

import "testing"

const toolResponse = `{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"checking","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"weather","arguments":"{\"city\":\"上海\"}"},{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_2","name":"weather","arguments":"{}"}],"usage":{"input_tokens":20,"output_tokens":8,"input_tokens_details":{"cached_tokens":5}}}`

func TestResponseToolsAndUsage(t *testing.T) {
	b, err := ConvertResponse([]byte(toolResponse), "claude-alias")
	if err != nil {
		t.Fatal(err)
	}
	v := decode(t, b)
	if v["type"] != "message" || v["model"] != "claude-alias" || v["stop_reason"] != "tool_use" || v["stop_sequence"] != nil {
		t.Fatal(v)
	}
	blocks := v["content"].([]any)
	if len(blocks) != 3 || blocks[1].(map[string]any)["id"] != "call_1" {
		t.Fatal(blocks)
	}
	if blocks[1].(map[string]any)["input"].(map[string]any)["city"] != "上海" {
		t.Fatal(blocks)
	}
	usage := v["usage"].(map[string]any)
	if usage["input_tokens"] != float64(15) || usage["cache_read_input_tokens"] != float64(5) || usage["output_tokens"] != float64(8) {
		t.Fatal(usage)
	}
}

func TestResponseStopReasons(t *testing.T) {
	cases := []struct{ raw, stop string }{
		{`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"你好"}]}]}`, "end_turn"},
		{`{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"part"}]}]}`, "max_tokens"},
		{`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot help"}]}]}`, "refusal"},
		{`{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, "refusal"},
	}
	for _, tc := range cases {
		b, e := ConvertResponse([]byte(tc.raw), "m")
		if e != nil {
			t.Fatal(e)
		}
		if decode(t, b)["stop_reason"] != tc.stop {
			t.Fatal(string(b))
		}
	}
}

func TestResponseRejectsInvalidUpstream(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `{"id":"r","status":"failed","error":{"message":"bad"}}`,
		`{"id":"r","status":"in_progress","output":[]}`,
		`{"id":"r","status":"completed","output":[{"type":"web_search_call"}]}`,
		`{"id":"r","status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"[]"}]}`,
		`{"id":"r","status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"{"}]}`,
	} {
		if _, e := ConvertResponse([]byte(raw), "m"); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
