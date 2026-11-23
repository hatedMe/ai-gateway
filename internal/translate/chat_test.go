package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatRefusalAndFilteredOutput(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		raw := `{"id":"resp_refusal","status":"` + status + `","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"Cannot comply"}]}]}`
		converted, err := ConvertResponseForChat([]byte(raw), "model")
		if err != nil {
			t.Fatal(err)
		}
		out, err := ChatResponse(converted)
		if err != nil {
			t.Fatal(err)
		}
		finish := "stop"
		if status == "incomplete" {
			finish = "content_filter"
		}
		if !strings.Contains(string(out), `"refusal":"Cannot comply"`) || !strings.Contains(string(out), `"finish_reason":"`+finish+`"`) {
			t.Fatal(string(out))
		}
		var chunks []string
		bridge := NewChatStream("model", false, func(v any) error { b, _ := json.Marshal(v); chunks = append(chunks, string(b)); return nil })
		event := "response.completed"
		if status == "incomplete" {
			event = "response.incomplete"
		}
		stream := "data: {\"type\":\"" + event + "\",\"response\":" + raw + "}\n\n"
		if err = ConvertStreamForChat(strings.NewReader(stream), "model", bridge.Emit); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(chunks, "\n")
		if !strings.Contains(joined, `"refusal":"Cannot comply"`) || !strings.Contains(joined, `"finish_reason":"`+finish+`"`) {
			t.Fatal(joined)
		}
	}
	_, _, err := PrepareChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"refusal","refusal":"Cannot comply"}]},{"role":"user","content":"try again"}]}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatLargeToolIntegerAndCitations(t *testing.T) {
	raw := []byte(`{"id":"msg_x","model":"m","stop_reason":"tool_use","content":[{"type":"text","text":"source","citations":[{"type":"web_search_result_location","url":"https://example.com"}]},{"type":"tool_use","id":"c","name":"f","input":{"id":9007199254740993}}]}`)
	out, err := ChatResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "9007199254740993") || !strings.Contains(string(out), `"citations":[`) {
		t.Fatal(string(out))
	}
	request := []byte(`{"model":"m","messages":[{"role":"user","content":"run"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"id\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"c","content":"ok"}]}`)
	p, _, err := PrepareChatRequest(request, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Body), "9007199254740993") {
		t.Fatal(string(p.Body))
	}
}

func TestPrepareChatRequestToolsAndOptions(t *testing.T) {
	raw := []byte(`{"model":"client","max_completion_tokens":2048,"reasoning_effort":"xhigh","stream":true,"stream_options":{"include_usage":true},"response_format":{"type":"json_object"},"messages":[{"role":"system","content":"be concise"},{"role":"user","content":"run tools"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"b","arguments":"{\"x\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"first"},{"role":"tool","tool_call_id":"c2","content":"second"}],"tools":[{"type":"function","function":{"name":"a","parameters":{"type":"object","properties":{}}}},{"type":"function","function":{"name":"b","parameters":{"type":"object","properties":{"x":{"type":"integer"}}}}}],"tool_choice":"auto"}`)
	p, usage, err := PrepareChatRequest(raw, RequestOptions{Model: "upstream"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(p.Body, &out)
	if !usage || out["model"] != "upstream" || out["max_output_tokens"] != float64(2048) || out["reasoning"].(map[string]any)["effort"] != "xhigh" || out["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Fatal(string(p.Body))
	}
	input := out["input"].([]any)
	if len(input) != 6 || input[4].(map[string]any)["call_id"] != "c1" || input[5].(map[string]any)["call_id"] != "c2" {
		t.Fatal(string(p.Body))
	}
}

func TestPrepareChatRequestValidation(t *testing.T) {
	for _, extra := range []string{`"n":2`, `"stream_options":{"include_usage":"yes"}`, `"reasoning_effort":"invalid"`, `"max_tokens":0`, `"functions":[]`, `"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{"}}]}]`} {
		_, _, err := PrepareChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],`+extra+`}`), RequestOptions{})
		if err == nil {
			t.Fatal("accepted invalid Chat request", extra)
		}
	}
}

func TestPrepareChatImagesAndDefaults(t *testing.T) {
	p, _, err := PrepareChatRequest([]byte(`{"model":"m","reasoning_effort":"minimal","tool_choice":"none","parallel_tool_calls":false,"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}]}]}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(p.Body, &out)
	if out["max_output_tokens"] != float64(4096) || out["reasoning"].(map[string]any)["effort"] != "minimal" {
		t.Fatal(string(p.Body))
	}
	image := out["input"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)
	if image["type"] != "input_image" || image["detail"] != "high" {
		t.Fatal(image)
	}
}

func TestChatResponseAndStream(t *testing.T) {
	raw := []byte(`{"id":"msg_test","type":"message","model":"client","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"c","name":"f","input":{"x":1}}],"stop_reason":"tool_use","usage":{"input_tokens":8,"cache_read_input_tokens":2,"output_tokens":3}}`)
	converted, err := ChatResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(converted, &out)
	choice := out["choices"].([]any)[0].(map[string]any)
	if out["object"] != "chat.completion" || choice["finish_reason"] != "tool_calls" || out["usage"].(map[string]any)["prompt_tokens"] != float64(10) {
		t.Fatal(string(converted))
	}
	var chunks []any
	stream := NewChatStream("client", true, func(v any) error { chunks = append(chunks, v); return nil })
	if err = ConvertStream(strings.NewReader(toolStream()), "client", stream.Emit); err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || chunks[len(chunks)-1] != nil {
		t.Fatal("missing DONE")
	}
	encoded, _ := json.Marshal(chunks)
	if !strings.Contains(string(encoded), `"object":"chat.completion.chunk"`) || !strings.Contains(string(encoded), `"finish_reason":"tool_calls"`) || !strings.Contains(string(encoded), `"choices":[]`) {
		t.Fatal(string(encoded))
	}
	var failed []any
	s := NewChatStream("m", false, func(v any) error { failed = append(failed, v); return nil })
	if err = ConvertStream(strings.NewReader(frame(`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`)), "m", s.Emit); err == nil {
		t.Fatal("accepted truncated stream")
	}
	for _, v := range failed {
		if v == nil {
			t.Fatal("false DONE on failure")
		}
	}
}
