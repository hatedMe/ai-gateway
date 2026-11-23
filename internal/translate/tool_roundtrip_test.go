package translate

import (
	"encoding/json"
	"testing"
)

func TestToolHistoryValidation(t *testing.T) {
	for _, input := range []string{
		`[{"type":"function_call_output","call_id":"orphan","output":"ok"}]`,
		`[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"}]`,
		`[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"type":"function_call","call_id":"a","name":"f","arguments":"{}"}]`,
		`[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"role":"user","content":"next"},{"type":"function_call_output","call_id":"a","output":"late"}]`,
	} {
		_, err := EncodeUpstreamRequest(ChatProtocol, []byte(`{"model":"m","input":`+input+`}`))
		if err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}
func TestToolRoundTripInterleaving(t *testing.T) {
	input := `[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"role":"assistant","content":"between"},{"type":"function_call","call_id":"b","name":"g","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"B"},{"type":"function_call_output","call_id":"a","output":"A"},{"role":"user","content":"next"}]`
	body, err := EncodeUpstreamRequest(ChatProtocol, []byte(`{"model":"m","input":`+input+`}`))
	if err != nil {
		t.Fatal(err)
	}
	var root object
	_ = json.Unmarshal(body, &root)
	messages := root["messages"].([]any)
	if len(messages) != 6 || messages[1].(map[string]any)["content"] != "between" {
		t.Fatal(string(body))
	}
}
