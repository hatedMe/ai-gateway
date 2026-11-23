package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-gateway-go/internal/translate"
)

func TestHiddenThinkingNormalAndStreamPreserveReplay(t *testing.T) {
	upstream := `{"id":"resp_display","status":"completed","output":[{"id":"rs","type":"reasoning","summary":[{"type":"summary_text","text":"private summary"}],"encrypted_content":"opaque"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	converted, err := translate.ConvertResponse([]byte(upstream), "m")
	if err != nil {
		t.Fatal(err)
	}
	prepared := translate.PreparedRequest{OmitThinking: true}
	normal, err := transformResponse(converted, prepared)
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	_ = json.Unmarshal(normal, &message)
	thinking := message["content"].([]any)[0].(map[string]any)
	if thinking["thinking"] != "" || strings.Contains(string(normal), "private summary") {
		t.Fatal("thinking leaked")
	}
	var events []map[string]any
	f := newOutputFilter(prepared, func(name string, p any) error {
		raw, _ := json.Marshal(p)
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		v["event"] = name
		events = append(events, v)
		return nil
	})
	stream := `data: {"type":"response.created","response":{"id":"resp_display","status":"in_progress"}}` + "\n\n" + `data: {"type":"response.completed","response":` + upstream + `}` + "\n\n"
	if err = translate.ConvertStream(strings.NewReader(stream), "m", f.Emit); err != nil {
		t.Fatal(err)
	}
	var signature string
	for _, e := range events {
		if delta, ok := e["delta"].(map[string]any); ok {
			if delta["type"] == "thinking_delta" {
				t.Fatal("hidden delta leaked")
			}
			if delta["type"] == "signature_delta" {
				signature = delta["signature"].(string)
			}
		}
	}
	if signature != thinking["signature"] {
		t.Fatal("normal and stream signatures differ")
	}
	history, _ := json.Marshal(map[string]any{"model": "m", "max_tokens": 128, "messages": []any{map[string]any{"role": "assistant", "content": message["content"]}, map[string]any{"role": "user", "content": "next"}}})
	replay, err := translate.PrepareRequest(history, translate.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replay.Body), `"encrypted_content":"opaque"`) || !strings.Contains(string(replay.Body), "private summary") {
		t.Fatal("hidden reasoning state lost")
	}
}
