package translate

import (
	"encoding/json"
	"testing"
)

// No placeholder fields are currently injected. Cleanup must remain identity.
func TestToolArgumentCleanup(t *testing.T) {
	for _, args := range []string{`{"reason":"business","_":"value"}`, `{"dynamic":{"reason":true}}`, `{"choice":{"_":2,"reason":"union"}}`} {
		raw, _ := json.Marshal(object{"id": "c", "model": "m", "choices": []object{{"message": object{"tool_calls": []object{{"id": "a", "function": object{"name": "f", "arguments": args}}}}, "finish_reason": "tool_calls"}}})
		body, err := DecodeUpstreamResponse(ChatProtocol, raw)
		if err != nil {
			t.Fatal(err)
		}
		var root struct{ Output []struct{ Arguments string } }
		_ = json.Unmarshal(body, &root)
		if root.Output[0].Arguments != args {
			t.Fatal(string(body))
		}
	}
}
