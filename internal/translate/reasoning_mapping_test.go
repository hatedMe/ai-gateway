package translate

import (
	"reflect"
	"testing"
)

func TestReasoningIntentMapping(t *testing.T) {
	effort := ReasoningCapabilities{Mode: "effort", Efforts: []string{"none", "low", "high"}}
	budget := ReasoningCapabilities{Mode: "budget", Efforts: []string{"none", "low", "high"}, Budgets: map[string]int{"low": 1024, "high": 3072}}
	for _, tc := range []struct {
		name   string
		intent ReasoningIntent
		target Protocol
		caps   ReasoningCapabilities
		limit  int
		want   map[string]any
		bad    bool
	}{
		{"absent", ReasoningIntent{}, ChatProtocol, ReasoningCapabilities{}, 0, nil, false},
		{"chat", ReasoningIntent{Present: true, Effort: "high"}, ChatProtocol, effort, 4096, map[string]any{"reasoning_effort": "high"}, false},
		{"responses-disable", ReasoningIntent{Present: true, Disabled: true}, ResponsesProtocol, effort, 4096, map[string]any{"reasoning": map[string]any{"effort": "none"}}, false},
		{"anthropic-disable", ReasoningIntent{Present: true, Disabled: true}, AnthropicProtocol, budget, 4096, map[string]any{"thinking": map[string]any{"type": "disabled"}}, false},
		{"mapped-budget", ReasoningIntent{Present: true, Effort: "high"}, AnthropicProtocol, budget, 4096, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 3072}}, false},
		{"exact-budget", ReasoningIntent{Present: true, BudgetTokens: 2345}, AnthropicProtocol, budget, 4096, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2345}}, false},
		{"no-capability", ReasoningIntent{Present: true, Effort: "high"}, ChatProtocol, ReasoningCapabilities{}, 4096, nil, true},
		{"no-clamp", ReasoningIntent{Present: true, Effort: "medium"}, ChatProtocol, effort, 4096, nil, true},
		{"budget-to-effort", ReasoningIntent{Present: true, BudgetTokens: 2048}, ResponsesProtocol, effort, 4096, nil, true},
		{"equal-limit", ReasoningIntent{Present: true, BudgetTokens: 2048}, AnthropicProtocol, budget, 2048, nil, true},
		{"over-limit", ReasoningIntent{Present: true, BudgetTokens: 4096}, AnthropicProtocol, budget, 2048, nil, true},
		{"missing-limit", ReasoningIntent{Present: true, BudgetTokens: 2048}, AnthropicProtocol, budget, 0, nil, true},
		{"small-budget", ReasoningIntent{Present: true, BudgetTokens: 1}, AnthropicProtocol, budget, 4096, nil, true},
		{"mapped-equal-limit", ReasoningIntent{Present: true, Effort: "high"}, AnthropicProtocol, budget, 3072, nil, true},
		{"mapped-small-budget", ReasoningIntent{Present: true, Effort: "low"}, AnthropicProtocol, ReasoningCapabilities{Mode: "budget", Efforts: []string{"low"}, Budgets: map[string]int{"low": 512}}, 4096, nil, true},
		{"missing-level-budget", ReasoningIntent{Present: true, Effort: "low"}, AnthropicProtocol, ReasoningCapabilities{Mode: "budget", Efforts: []string{"low"}}, 4096, nil, true},
		{"conflict", ReasoningIntent{Present: true, Effort: "high", BudgetTokens: 2048}, AnthropicProtocol, budget, 4096, nil, true},
		{"undeclared-disable", ReasoningIntent{Present: true, Disabled: true}, ChatProtocol, ReasoningCapabilities{Mode: "effort", Efforts: []string{"high"}}, 4096, nil, true},
		{"wrong-mode", ReasoningIntent{Present: true, Effort: "high"}, ChatProtocol, budget, 4096, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeReasoningIntent(tc.intent, tc.target, tc.caps, tc.limit)
			if tc.bad {
				assertReasoningError(t, err)
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, tc.want)
			}
		})
	}
	// Identical upstream IDs do not matter: only the passed capability snapshot applies.
	if _, err := EncodeReasoningIntent(ReasoningIntent{Present: true, Effort: "high"}, ChatProtocol, ReasoningCapabilities{Mode: "effort", Efforts: []string{"low"}}, 4096); err == nil {
		t.Fatal("capabilities leaked between routes")
	}
}
