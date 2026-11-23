package translate

import (
	"errors"
	"strings"
	"testing"
)

func TestReasoningIntentMappingParse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol Protocol
		raw      string
		want     ReasoningIntent
		bad      bool
	}{
		{"absent", ChatProtocol, `{}`, ReasoningIntent{}, false},
		{"chat", ChatProtocol, `{"reasoning_effort":"high"}`, ReasoningIntent{Present: true, Effort: "high"}, false},
		{"disabled", ResponsesProtocol, `{"reasoning":{"effort":"none"}}`, ReasoningIntent{Present: true, Disabled: true}, false},
		{"budget", AnthropicProtocol, `{"thinking":{"type":"enabled","budget_tokens":3072}}`, ReasoningIntent{Present: true, BudgetTokens: 3072}, false},
		{"effort", AnthropicProtocol, `{"output_config":{"effort":"medium"}}`, ReasoningIntent{Present: true, Effort: "medium"}, false},
		{"anthropic-disabled", AnthropicProtocol, `{"thinking":{"type":"disabled"}}`, ReasoningIntent{Present: true, Disabled: true}, false},
		{"conflict", AnthropicProtocol, `{"thinking":{"type":"enabled","budget_tokens":2048},"output_config":{"effort":"low"}}`, ReasoningIntent{}, true},
		{"disabled-conflict", AnthropicProtocol, `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, ReasoningIntent{}, true},
		{"disabled-budget", AnthropicProtocol, `{"thinking":{"type":"disabled","budget_tokens":2048}}`, ReasoningIntent{}, true},
		{"small", AnthropicProtocol, `{"thinking":{"type":"enabled","budget_tokens":1023}}`, ReasoningIntent{}, true},
		{"fraction", AnthropicProtocol, `{"thinking":{"type":"enabled","budget_tokens":2048.5}}`, ReasoningIntent{}, true},
		{"source-limit", AnthropicProtocol, `{"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":2048}}`, ReasoningIntent{}, true},
		{"missing-budget", AnthropicProtocol, `{"thinking":{"type":"enabled"}}`, ReasoningIntent{}, true},
		{"adaptive-unsupported", AnthropicProtocol, `{"thinking":{"type":"adaptive"}}`, ReasoningIntent{}, true},
		{"overflow", AnthropicProtocol, `{"thinking":{"type":"enabled","budget_tokens":99999999999999999999999999999999999999}}`, ReasoningIntent{}, true},
		{"null", ResponsesProtocol, `{"reasoning":null}`, ReasoningIntent{}, true},
		{"null-effort", ChatProtocol, `{"reasoning_effort":null}`, ReasoningIntent{}, true},
		{"unsafe", ChatProtocol, `{"reasoning_effort":"SECRET_SENTINEL"}`, ReasoningIntent{}, true},
		{"foreign-conflict", ChatProtocol, `{"reasoning_effort":"high","reasoning":{"effort":"low"}}`, ReasoningIntent{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReasoningIntent([]byte(tc.raw), tc.protocol)
			if tc.bad {
				assertReasoningError(t, err)
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func assertReasoningError(t *testing.T, err error) {
	t.Helper()
	var ce *ConversionError
	if !errors.As(err, &ce) || ce.Stage != "request" || ce.Code != "unsupported_capability" || ce.Field == "" || strings.Contains(err.Error(), "SECRET_SENTINEL") {
		t.Fatalf("unsafe/untyped reasoning error: %v", err)
	}
}

func TestRejectCrossProviderReasoningReplay(t *testing.T) {
	for _, tc := range []struct {
		protocol Protocol
		raw      string
		bad      bool
	}{
		{ResponsesProtocol, `{"previous_response_id":"resp_secret"}`, true},
		{ResponsesProtocol, `{"input":[{"type":"reasoning","encrypted_content":"secret"}]}`, true},
		{ResponsesProtocol, `{"input":[{"type":"reasoning","id":"rs_secret","summary":[]}]}`, true},
		{AnthropicProtocol, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"secret"}]}]}`, true},
		{AnthropicProtocol, `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"secret"}]}]}`, true},
		{ChatProtocol, `{"messages":[{"role":"assistant","reasoning_content":"readable"}]}`, false},
		{ResponsesProtocol, `{"input":[{"type":"function_call","arguments":"{\"signature\":\"business\"}"}]}`, false},
		{AnthropicProtocol, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","input":{"signature":"business","reasoning":{"encrypted_content":"business"}}}]}]}`, false},
		{ResponsesProtocol, `{"tools":[{"parameters":{"properties":{"previous_response_id":{"type":"string"}}}}]}`, false},
	} {
		err := RejectCrossProviderReasoningReplay([]byte(tc.raw), tc.protocol)
		if tc.bad {
			assertReasoningError(t, err)
		} else if err != nil {
			t.Fatalf("business data rejected: %v", err)
		}
	}
}
