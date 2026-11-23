package translate

import (
	"encoding/json"
	"testing"
)

func TestThinkingDisplayRequest(t *testing.T) {
	for _, display := range []string{"summarized", "omitted", "updates"} {
		p, err := PrepareRequest([]byte(`{"model":"m","max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024,"display":"`+display+`"},"messages":[{"role":"user","content":"q"}]}`), RequestOptions{})
		if err != nil {
			t.Fatal(display, err)
		}
		var out struct {
			Reasoning map[string]any `json:"reasoning"`
		}
		_ = json.Unmarshal(p.Body, &out)
		if p.OmitThinking != (display != "summarized") {
			t.Fatal("wrong display policy", display)
		}
		if display == "summarized" && out.Reasoning["summary"] != "auto" {
			t.Fatal("missing summary")
		}
		if display != "summarized" && out.Reasoning["summary"] != nil {
			t.Fatal("hidden thinking still requests summaries")
		}
		if display == "updates" && len(p.Warnings) == 0 {
			t.Fatal("missing progress downgrade warning")
		}
	}
}

func TestOmittedThinkingCarrierRoundTrip(t *testing.T) {
	item := upstreamItem{ID: "rs", Type: "reasoning", Summary: []upstreamPart{{Type: "summary_text", Text: "original summary"}}, EncryptedContent: "opaque"}
	block, err := item.thinkingBlock()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := OmitThinkingSignature(block["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(object{"type": "thinking", "thinking": "", "signature": signature})
	fields, _ := rawObject(raw)
	restored, err := reasoningHistory(fields)
	if err != nil {
		t.Fatal(err)
	}
	if restored["encrypted_content"] != "opaque" || restored["id"] != "rs" {
		t.Fatal(restored)
	}
	raw, _ = json.Marshal(object{"type": "thinking", "thinking": "tampered", "signature": signature})
	fields, _ = rawObject(raw)
	if _, err = reasoningHistory(fields); err == nil {
		t.Fatal("accepted changed omitted thinking")
	}
}
