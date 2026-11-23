package translate

import (
	"encoding/json"
	"testing"
)

func TestInstructionRolesPreserveMessageOrder(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":128,"system":"top","messages":[{"role":"user","content":"question"},{"role":"system","content":[{"type":"text","text":"late system"}]},{"role":"developer","content":"later instruction"}]}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err = json.Unmarshal(p.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Input) != 4 {
		t.Fatalf("lost messages: %s", p.Body)
	}
	roles := []string{"developer", "user", "developer", "developer"}
	texts := []string{"top", "question", "late system", "later instruction"}
	for i, item := range out.Input {
		if item.Role != roles[i] || len(item.Content) != 1 || item.Content[0].Type != "input_text" || item.Content[0].Text != texts[i] {
			t.Fatalf("wrong order or role: %s", p.Body)
		}
	}
}

func TestInstructionRoleRejectsNonText(t *testing.T) {
	for _, content := range []string{`[]`, `[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]`, `[{"type":"tool_use","id":"call","name":"f","input":{}}]`} {
		_, err := PrepareRequest([]byte(`{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"q"},{"role":"system","content":`+content+`}]}`), RequestOptions{})
		if err == nil {
			t.Fatal("accepted non-text instruction", content)
		}
	}
}

func TestInstructionRoleDoesNotLosePendingTools(t *testing.T) {
	p, err := PrepareRequest([]byte(`{"model":"m","max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":{}}]},{"role":"system","content":"instruction"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"done"}]}]}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Input []map[string]any `json:"input"`
	}
	_ = json.Unmarshal(p.Body, &out)
	if len(out.Input) != 3 || out.Input[0]["type"] != "function_call" || out.Input[1]["role"] != "developer" || out.Input[2]["call_id"] != "c" {
		t.Fatal(string(p.Body))
	}
}
