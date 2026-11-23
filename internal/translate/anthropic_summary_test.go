package translate

import (
	"strings"
	"testing"
)

func TestAnthropicReadableSummary(t *testing.T) {
	raw := []byte(`{"id":"a","model":"m","content":[{"type":"thinking","thinking":"analysis","signature":"native-secret"},{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`)
	body, err := DecodeUpstreamResponse(AnthropicProtocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"summary"`) || strings.Contains(string(body), "native-secret") {
		t.Fatal(string(body))
	}
	frames := chatFrames(`{"type":"message_start","message":{"id":"a","model":"m"}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"analysis"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"native-secret"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)
	var out strings.Builder
	err = DecodeUpstreamStream(AnthropicProtocol, strings.NewReader(frames), func(name string, v any) error { out.WriteString(name); return nil })
	if err != nil || !strings.Contains(out.String(), "reasoning_summary_text.delta") {
		t.Fatal(err, out.String())
	}
}
