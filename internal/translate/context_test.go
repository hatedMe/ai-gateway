package translate

import (
	"errors"
	"strings"
	"testing"
)

func TestConversionContextIsolation(t *testing.T) {
	a := ConversionContext{Source: ResponsesProtocol, Target: ChatProtocol, ClientModel: "public-a", UpstreamModel: "real", Capabilities: ModelCapabilities{ChatTokenLimitField: "max_tokens"}}
	b := ConversionContext{Source: ResponsesProtocol, Target: ChatProtocol, ClientModel: "public-b", UpstreamModel: "real"}
	raw := []byte(`{"model":"real","input":"hi","max_output_tokens":12}`)
	for _, c := range []*ConversionContext{&a, &b, &a} {
		encoded, err := EncodeUpstreamRequestWithContext(raw, c)
		if err != nil {
			t.Fatal(err)
		}
		field := c.Capabilities.ChatTokenLimitField
		if field == "" {
			field = "max_completion_tokens"
		}
		if !strings.Contains(string(encoded), `"`+field+`":12`) {
			t.Fatal(string(encoded))
		}
		decoded, err := DecodeUpstreamResponseWithContext([]byte(`{"id":"c","model":"real","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`), c)
		if err != nil || !strings.Contains(string(decoded), c.ClientModel) {
			t.Fatal(string(decoded), err)
		}
	}
}

func TestConversionErrors(t *testing.T) {
	c := &ConversionContext{Source: ResponsesProtocol, Target: ChatProtocol}
	_, err := EncodeUpstreamRequestWithContext([]byte(`{"model":"m","input":"secret-body","private_key":"secret-key"}`), c)
	var conversion *ConversionError
	if !errors.As(err, &conversion) || conversion.Code != "unsupported_field" || conversion.Stage != "request" || conversion.Field != "private_key" {
		t.Fatalf("%#v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe error")
	}
	for _, tc := range []struct{ call, code string }{
		{`{"id":"","function":{"name":"f","arguments":"{}"}}`, "invalid_tool_identity"},
		{`{"id":"a","function":{"name":"f","arguments":"secret-arguments"}}`, "invalid_tool_arguments"},
	} {
		_, err = DecodeUpstreamResponseWithContext([]byte(`{"id":"c","choices":[{"message":{"tool_calls":[`+tc.call+`]},"finish_reason":"tool_calls"}]}`), c)
		if !errors.As(err, &conversion) || conversion.Code != tc.code || conversion.Stage != "response" || conversion.Field == "" || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	err = DecodeUpstreamStreamWithContext(strings.NewReader(""), c, func(string, any) error { return nil })
	if !errors.As(err, &conversion) || conversion.Code != "incomplete_stream" || conversion.Stage != "stream" {
		t.Fatal(err)
	}
}
