package translate

import (
	"strings"
	"testing"
)

func TestReasoningDisplayIsNotSilentlyDropped(t *testing.T) {
	c := &ConversionContext{Source: ResponsesProtocol, Target: ChatProtocol, Capabilities: ModelCapabilities{Reasoning: &ReasoningCapabilities{Mode: "effort", Efforts: []string{"low"}}}}
	_, err := PrepareClientCapabilities([]byte(`{"model":"m","input":"hi","reasoning":{"effort":"low","summary":"detailed"}}`), c)
	if err == nil {
		t.Fatal("silently discarded summary preference")
	}
	c = &ConversionContext{Source: AnthropicProtocol, Target: ChatProtocol, Capabilities: ModelCapabilities{Reasoning: &ReasoningCapabilities{Mode: "effort", Efforts: []string{"none"}}}}
	_, err = PrepareClientCapabilities([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled","display":"invalid"}}`), c)
	if err == nil {
		t.Fatal("accepted invalid display")
	}
}

func TestSummaryToAnthropicHasNoSignature(t *testing.T) {
	c := &ConversionContext{Source: AnthropicProtocol, Target: ChatProtocol}
	raw := []byte(`{"id":"c","model":"real","choices":[{"message":{"reasoning_content":"analysis","content":"answer"},"finish_reason":"stop"}]}`)
	normalized, err := DecodeUpstreamResponseWithContext(raw, c)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ConvertResponse(normalized, "public")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(message), "signature") || !strings.Contains(string(message), "analysis") {
		t.Fatal(string(message))
	}
	c.omitThinking = true
	normalized, err = DecodeUpstreamResponseWithContext(raw, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normalized), "analysis") {
		t.Fatal("omitted summary remained visible")
	}
}
