package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareContextManagementKeepsAllThinking(t *testing.T) {
	base := `{"model":"m","max_tokens":2048,"messages":[{"role":"user","content":"test"}]`
	without, err := PrepareRequest([]byte(base+`}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	with, err := PrepareRequest([]byte(base+`,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(with.Body) != string(without.Body) {
		t.Fatalf("keep all changed history: %s", with.Body)
	}
	if len(with.Warnings) != 0 {
		t.Fatal("no-op should not warn", with.Warnings)
	}
}

func TestPrepareContextManagementUnmappedEditsPreserveHistory(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"history must stay"}],"context_management":{"edits":[{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses","value":3}}]}}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(p.Body, &out)
	if _, ok := out["context_management"]; ok {
		t.Fatal("Anthropic field leaked upstream")
	}
	if !strings.Contains(string(p.Body), "history must stay") || len(p.Warnings) == 0 {
		t.Fatalf("history or warning lost: %s %v", p.Body, p.Warnings)
	}
}

func TestPrepareContextManagementValidation(t *testing.T) {
	for _, value := range []string{`[]`, `true`, `{"edits":{}}`, `{"edits":[null]}`, `{"edits":[{}]}`, `{"edits":[{"type":2}]}`} {
		_, err := PrepareRequest([]byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"test"}],"context_management":`+value+`}`), RequestOptions{})
		if err == nil {
			t.Fatal("accepted invalid context config", value)
		}
	}
}
