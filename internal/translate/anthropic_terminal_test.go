package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicRefusalTerminalParity(t *testing.T) {
	raw := []byte(`{"id":"a","model":"m","content":[{"type":"text","text":"cannot comply"}],"stop_reason":"refusal"}`)
	body, err := DecodeUpstreamResponse(AnthropicProtocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	assertRefusal := func(body []byte) {
		message, err := ConvertResponse(body, "public")
		if err != nil {
			t.Fatal(err)
		}
		var root object
		_ = json.Unmarshal(message, &root)
		if root["stop_reason"] != "refusal" {
			t.Fatal(string(message))
		}
	}
	assertRefusal(body)
	frames := chatFrames(`{"type":"message_start","message":{"id":"a","model":"m"}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"cannot comply"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"refusal"}}`, `{"type":"message_stop"}`)
	err = DecodeUpstreamStream(AnthropicProtocol, strings.NewReader(frames), func(name string, v any) error {
		if name == "response.completed" {
			event := v.(map[string]any)
			body, _ := json.Marshal(event["response"])
			assertRefusal(body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
