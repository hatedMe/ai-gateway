package translate

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTruncatedToolResponseAndStream(t *testing.T) {
	raw := `{"id":"resp_x","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"before"}]},{"type":"function_call","call_id":"c","name":"lookup","arguments":"{\"q\":"}]}`
	b, err := ConvertResponse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"type":"tool_use"`) || !strings.Contains(string(b), "Incomplete tool call") || !strings.Contains(string(b), "max_tokens") {
		t.Fatal(string(b))
	}
	s := `data: {"type":"response.created","response":{"id":"resp_x"}}` + "\n\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"lookup"}}` + "\n\n" + `data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":"}` + "\n\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","status":"incomplete","call_id":"c","name":"lookup","arguments":"{\"q\":"}}` + "\n\n" + `data: {"type":"response.incomplete","response":{"id":"resp_x","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"c","name":"lookup","arguments":"{\"q\":"}]}}` + "\n\n"
	var events []string
	err = ConvertStream(strings.NewReader(s), "", func(name string, v any) error {
		b, _ := json.Marshal(v)
		events = append(events, name+":"+string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, "partial_json") || !strings.Contains(joined, "max_tokens") || !strings.HasPrefix(events[len(events)-1], "message_stop:") {
		t.Fatal(joined)
	}
	completed := strings.ReplaceAll(raw, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, `"status":"completed"`)
	if _, err = ConvertResponse([]byte(completed), ""); err == nil {
		t.Fatal("completed malformed arguments accepted")
	}
}

func TestDirectReasoningCarrierAndSummaryPreference(t *testing.T) {
	for _, summary := range []string{`[]`, `[{"type":"summary_text","text":"summary"}]`} {
		raw := `{"id":"resp_r","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":` + summary + `,"content":[{"type":"reasoning_text","text":"direct"}],"encrypted_content":"opaque"}]}`
		b, err := ConvertResponse([]byte(raw), "")
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Content []map[string]json.RawMessage `json:"content"`
		}
		json.Unmarshal(b, &m)
		history, err := reasoningHistory(m.Content[0])
		if err != nil {
			t.Fatal(err)
		}
		if history["content"] == nil || history["encrypted_content"] != "opaque" || history["id"] != "rs_1" {
			t.Fatal(history)
		}
		want := "direct"
		if summary != `[]` {
			want = "summary"
		}
		if string(m.Content[0]["thinking"]) != `"`+want+`"` {
			t.Fatal(string(b))
		}
		var out []byte
		s := "data: {\"type\":\"response.completed\",\"response\":" + raw + "}\n\n"
		err = ConvertStream(strings.NewReader(s), "", func(_ string, v any) error { b, _ := json.Marshal(v); out = append(out, b...); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"thinking":"`+want+`"`) {
			t.Fatal(string(out))
		}
	}
}

func TestAggregateStreamAndTypedErrors(t *testing.T) {
	raw := `{"id":"r","status":"completed","output":[]}`
	got, err := AggregateStream(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":" + raw + "}\n\n"))
	if err != nil || string(got) != raw {
		t.Fatalf("%s %v", got, err)
	}
	for _, event := range []string{`{"type":"error","code":"rate_limit_exceeded","message":"slow","param":"model","request_id":"req_x"}`, `{"type":"response.failed","response":{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow","param":"model","request_id":"req_x"}}}`} {
		_, err = AggregateStream(strings.NewReader("data: " + event + "\n\n"))
		var upstream *UpstreamError
		if !errors.As(err, &upstream) || upstream.Code != "rate_limit_exceeded" || upstream.Param != "model" || upstream.RequestID != "req_x" {
			t.Fatal(err)
		}
	}
}

func TestBuiltinToolsAndCitations(t *testing.T) {
	raw := `{"id":"r","status":"completed","output":[{"id":"ws_1","type":"web_search_call","action":{"type":"search","query":"x"},"results":[{"url":"https://example.com","title":"Example"}]},{"type":"message","content":[{"type":"output_text","text":"citation","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":8}]}]},{"id":"ci_1","type":"code_interpreter_call","code":"print(1)","outputs":[{"type":"logs","logs":"1"}]}]}`
	b, err := ConvertResponse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server_tool_use", "web_search_tool_result", "web_search_result_location", "exit status unavailable", "logs"} {
		if !strings.Contains(string(b), want) {
			t.Fatal(string(b))
		}
	}
	var events string
	err = ConvertStream(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+raw+"}\n\n"), "", func(_ string, v any) error { b, _ := json.Marshal(v); events += string(b); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(events, "citations_delta") || !strings.Contains(events, "server_tool_use") {
		t.Fatal(events)
	}
}

func TestCitationOffsetsAndMissingDocumentMetadata(t *testing.T) {
	p := upstreamPart{Type: "output_text", Text: "你a cited tail", Annotations: []object{{"type": "url_citation", "url": "https://example.com", "start_index": float64(3), "end_index": float64(8)}, {"type": "file_citation", "file_id": "file_x", "filename": "paper.pdf"}}}
	cs := p.citations()
	if len(cs) != 1 || cs[0]["cited_text"] != "cited" {
		t.Fatal(cs)
	}
	v, err := p.value()
	if err != nil || !strings.Contains(v, "paper.pdf") || strings.Contains(v, "page_number") {
		t.Fatalf("%s %v", v, err)
	}
}

func TestCacheWriteUsage(t *testing.T) {
	var u upstreamUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":20,"output_tokens":3,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":7}}`), &u); err != nil {
		t.Fatal(err)
	}
	usage, err := u.anthropic()
	if err != nil || usage["input_tokens"] != 8 || usage["cache_creation_input_tokens"] != 7 || usage["cache_read_input_tokens"] != 5 {
		t.Fatal(usage, err)
	}
}

func TestMixedSearchResultsPreserveUnmappedRecords(t *testing.T) {
	raw := `{"id":"r","status":"completed","output":[{"type":"file_search_call","id":"fs_1","results":[{"url":"https://example.com","title":"web"},{"file_id":"file_x","text":"private content"}]}]}`
	b, err := ConvertResponse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "web_search_tool_result") || !strings.Contains(string(b), "private content") || !strings.Contains(string(b), "file_x") {
		t.Fatal(string(b))
	}
}

func TestDirectReasoningDeltaWaitsForSummary(t *testing.T) {
	for _, summary := range []bool{false, true} {
		frames := []string{`{"type":"response.created","response":{"id":"r"}}`, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs","type":"reasoning"}}`, `{"type":"response.reasoning_text.delta","output_index":0,"content_index":0,"delta":"direct"}`, `{"type":"response.reasoning_text.done","output_index":0,"content_index":0,"text":"direct"}`}
		summaryJSON := `[]`
		want := "direct"
		if summary {
			want = "summary"
			summaryJSON = `[{"type":"summary_text","text":"summary"}]`
			frames = append(frames, `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"summary"}`)
		}
		frames = append(frames, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"rs","type":"reasoning","summary":`+summaryJSON+`,"content":[{"type":"reasoning_text","text":"direct"}]}]}}`)
		var input, thinking string
		for _, f := range frames {
			input += "data: " + f + "\n\n"
		}
		err := ConvertStream(strings.NewReader(input), "", func(_ string, v any) error {
			b, _ := json.Marshal(v)
			var e struct {
				Delta struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			}
			json.Unmarshal(b, &e)
			if e.Delta.Type == "thinking_delta" {
				thinking += e.Delta.Thinking
			}
			return nil
		})
		if err != nil || thinking != want {
			t.Fatalf("%q %v", thinking, err)
		}
	}
}

func TestPartialToolItemDoneBeforeTerminal(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		frames := []string{`{"type":"response.created","response":{"id":"r"}}`, `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"lookup"}}`, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":"}`, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c","name":"lookup","arguments":"{\"x\":"}}`, `{"type":"response.` + status + `","response":{"id":"r","status":"` + status + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"c","name":"lookup","arguments":"{\"x\":"}]}}`}
		var input string
		for _, f := range frames {
			input += "data: " + f + "\n\n"
		}
		var names []string
		err := ConvertStream(strings.NewReader(input), "", func(name string, _ any) error { names = append(names, name); return nil })
		if status == "completed" {
			if err == nil {
				t.Fatal("malformed completed arguments accepted")
			}
			for _, name := range names {
				if name == "message_stop" {
					t.Fatal("message_stop after malformed completion")
				}
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			n := len(names)
			if n < 3 || names[n-3] != "content_block_stop" || names[n-2] != "message_delta" || names[n-1] != "message_stop" {
				t.Fatal(names)
			}
		}
	}
}
