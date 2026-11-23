package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestThinkingRequestModes(t *testing.T) {
	for _, tc := range []struct{ config, effort string }{
		{`"thinking":{"type":"enabled","budget_tokens":1024}`, "low"},
		{`"thinking":{"type":"enabled","budget_tokens":8192}`, "medium"},
		{`"thinking":{"type":"enabled","budget_tokens":16000}`, "high"},
		{`"thinking":{"type":"adaptive"}`, "high"},
		{`"thinking":{"type":"enabled","budget_tokens":25000}`, "xhigh"},
		{`"output_config":{"effort":"max"}`, "xhigh"},
		{`"thinking":{"type":"enabled","budget_tokens":4096},"output_config":{"effort":"low"}`, "low"},
		{`"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`, "low"},
		{`"thinking":{"type":"disabled"}`, "none"},
	} {
		raw := `{"model":"m","max_tokens":32000,"messages":[{"role":"user","content":"solve"}],` + tc.config + `}`
		b, e := ConvertRequest([]byte(raw), "")
		if e != nil {
			t.Fatal(e)
		}
		v := decode(t, b)
		reasoning := v["reasoning"].(map[string]any)
		if reasoning["effort"] != tc.effort {
			t.Fatal(v)
		}
		if tc.effort != "none" && reasoning["summary"] != "auto" {
			t.Fatal(v)
		}
		if tc.effort == "none" && reasoning["summary"] != nil {
			t.Fatal(v)
		}
		if !reflect.DeepEqual(v["include"], []any{"reasoning.encrypted_content"}) {
			t.Fatal(v)
		}
	}
}

func TestThinkingRejectsInvalidConfig(t *testing.T) {
	for _, config := range []string{
		`"thinking":null`, `"thinking":{"type":"enabled"}`,
		`"thinking":{"type":"enabled","budget_tokens":1023}`,
		`"thinking":{"type":"enabled","budget_tokens":32000}`,
		`"thinking":{"type":"adaptive","budget_tokens":1024}`,
		`"thinking":{"type":"disabled","budget_tokens":1024}`,
		`"thinking":{"type":"unknown"}`,
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"invalid"}`,
		`"output_config":{"effort":"unknown"}`,
		`"thinking":{"type":"disabled"},"output_config":{"effort":"low"}`,
	} {
		raw := `{"model":"m","max_tokens":32000,"messages":[{"role":"user","content":"hi"}],` + config + `}`
		if _, e := ConvertRequest([]byte(raw), ""); e == nil {
			t.Fatal(config)
		}
	}
}

const thinkingResponse = `{"id":"resp_think","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"first"},{"type":"summary_text","text":"second"}],"encrypted_content":"opaque-secret"},{"id":"fc_1","type":"function_call","call_id":"c1","name":"f","arguments":"{}"}],"usage":{"input_tokens":20,"output_tokens":10,"output_tokens_details":{"reasoning_tokens":7}}}`

func TestThinkingResponseAndToolHistory(t *testing.T) {
	b, e := ConvertResponse([]byte(thinkingResponse), "m")
	if e != nil {
		t.Fatal(e)
	}
	message := decode(t, b)
	blocks := message["content"].([]any)
	thinking := blocks[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "first\n\nsecond" || thinking["signature"] == "" {
		t.Fatal(thinking)
	}
	request := object{"model": "m", "max_tokens": 32000, "thinking": object{"type": "enabled", "budget_tokens": 4096}, "messages": []any{
		object{"role": "user", "content": "hi"},
		object{"role": "assistant", "content": blocks},
		object{"role": "user", "content": []object{{"type": "tool_result", "tool_use_id": "c1", "content": "ok"}}},
	}}
	raw, _ := json.Marshal(request)
	translated, e := ConvertRequest(raw, "")
	if e != nil {
		t.Fatal(e)
	}
	input := decode(t, translated)["input"].([]any)
	reasoning := input[1].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_1" || reasoning["encrypted_content"] != "opaque-secret" {
		t.Fatal(reasoning)
	}
	if len(reasoning["summary"].([]any)) != 2 {
		t.Fatal(reasoning)
	}
	if input[2].(map[string]any)["type"] != "function_call" || input[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatal(input)
	}
}

func thinkingStream() string {
	events := []string{
		`{"type":"response.created","response":{"id":"resp_think","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"fir"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"st"}`,
		`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"first"}`,
		`{"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"first"}}`,
		`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"second"}`,
		`{"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":"second"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"first"},{"type":"summary_text","text":"second"}],"encrypted_content":"opaque-secret"}}`,
		`{"type":"response.completed","response":` + thinkingResponse + `}`,
	}
	var s strings.Builder
	for _, e := range events {
		s.WriteString(frame(e))
	}
	return s.String()
}

func TestThinkingStreamMatchesResponse(t *testing.T) {
	events, e := runStream(t, thinkingStream())
	if e != nil {
		t.Fatal(e)
	}
	thinking, signature := "", ""
	starts, signatures := 0, 0
	for i, event := range events {
		if event.Name == "content_block_start" && event.Body["content_block"].(map[string]any)["type"] == "thinking" {
			starts++
		}
		if event.Name != "content_block_delta" {
			continue
		}
		delta := event.Body["delta"].(map[string]any)
		switch delta["type"] {
		case "thinking_delta":
			thinking += delta["thinking"].(string)
		case "signature_delta":
			signature += delta["signature"].(string)
			signatures++
			if i+1 >= len(events) || events[i+1].Name != "content_block_stop" {
				t.Fatal("signature must precede stop")
			}
		}
	}
	b, e := ConvertResponse([]byte(thinkingResponse), "claude-alias")
	if e != nil {
		t.Fatal(e)
	}
	normal := decode(t, b)["content"].([]any)[0].(map[string]any)
	if starts != 1 || signatures != 1 || thinking != normal["thinking"] || signature != normal["signature"] {
		t.Fatal(starts, signatures, thinking, signature, normal)
	}
}

func TestThinkingEncryptedOnlyAndEmptyReasoning(t *testing.T) {
	for _, payload := range []string{
		`{"id":"rs","type":"reasoning","summary":[],"encrypted_content":"opaque"}`,
		`{"id":"rs","type":"reasoning","summary":[]}`,
		`{"id":"rs","type":"reasoning","summary":[{"type":"summary_text","text":"summary only"}]}`,
	} {
		response := fmt.Sprintf(`{"id":"r","status":"completed","output":[%s,{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`, payload)
		b, e := ConvertResponse([]byte(response), "m")
		if e != nil {
			t.Fatal(e)
		}
		events, e := runStream(t, frame(`{"type":"response.completed","response":`+response+`}`))
		if e != nil {
			t.Fatal(e)
		}
		blocks := decode(t, b)["content"].([]any)
		starts := 0
		for _, event := range events {
			if event.Name == "content_block_start" {
				starts++
			}
		}
		if starts != len(blocks) {
			t.Fatal(events, blocks)
		}
	}
}

func TestThinkingMalformedHistory(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","thinking":4,"signature":"foreign-anthropic-signature"}`,
		`{"type":"thinking","thinking":"x","signature":"openai-reasoning-v1:not-base64!"}`,
		`{"type":"thinking","thinking":4}`,
	} {
		raw := `{"model":"m","max_tokens":4096,"messages":[{"role":"assistant","content":[` + block + `]},{"role":"user","content":"hi"}]}`
		if _, e := ConvertRequest([]byte(raw), ""); e == nil {
			t.Fatal(block)
		}
	}
}

func TestThinkingDeltasArriveBeforeFinalSignature(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	received := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- ConvertStream(reader, "m", func(name string, payload any) error {
			if name == "content_block_delta" {
				event := payload.(map[string]any)
				delta := event["delta"].(map[string]any)
				if delta["type"] == "thinking_delta" {
					select {
					case received <- struct{}{}:
					default:
					}
				}
			}
			return nil
		})
	}()
	prefix := strings.Split(thinkingStream(), `data: {"type":"response.output_item.done"`)[0]
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(writer, prefix); written <- err }()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("thinking buffered until terminal")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := <-finished; err == nil {
		t.Fatal("truncated thinking must not succeed")
	}
}

func TestThinkingStreamRejectsConflicts(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(thinkingStream(), `"text":"first"`, `"text":"conflict"`, 1),
		strings.Replace(thinkingStream(), `"summary_index":0,"delta":"fir"`, `"summary_index":2,"delta":"fir"`, 1),
		strings.Replace(thinkingStream(), `"encrypted_content":"opaque-secret"`, `"encrypted_content":""`, 1),
	} {
		events, err := runStream(t, raw)
		if err == nil {
			t.Fatal("accepted conflicting reasoning")
		}
		for _, event := range events {
			if event.Name == "message_stop" {
				t.Fatal("false success")
			}
		}
	}
}

func TestThinkingStreamAcceptsReencryptedFinalState(t *testing.T) {
	raw := strings.Replace(thinkingStream(), `"encrypted_content":"opaque-secret"`, `"encrypted_content":"reencrypted-item-done"`, 1)
	events, err := runStream(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Name != "message_stop" {
		t.Fatal("missing successful terminal")
	}
}

func TestThinkingSummaryOnlyHistory(t *testing.T) {
	response := `{"id":"r","status":"completed","output":[{"id":"rs","type":"reasoning","summary":[{"type":"summary_text","text":"summary"}]},{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`
	b, err := ConvertResponse([]byte(response), "m")
	if err != nil {
		t.Fatal(err)
	}
	history := decode(t, b)["content"]
	raw, _ := json.Marshal(object{"model": "m", "max_tokens": 4096, "messages": []object{{"role": "assistant", "content": history}, {"role": "user", "content": "next"}}})
	translated, err := ConvertRequest(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	input := decode(t, translated)["input"].([]any)
	for _, entry := range input {
		if entry.(map[string]any)["type"] == "reasoning" {
			t.Fatal("fabricated missing encrypted state")
		}
	}
	thinking := history.([]any)[0].(map[string]any)
	thinking["thinking"] = "modified"
	raw, _ = json.Marshal(object{"model": "m", "max_tokens": 4096, "messages": []object{{"role": "assistant", "content": history}, {"role": "user", "content": "next"}}})
	if _, err = ConvertRequest(raw, ""); err == nil {
		t.Fatal("accepted edited thinking history")
	}
}
