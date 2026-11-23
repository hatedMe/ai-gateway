package translate

// ReasoningCapabilities describes only the selected route's declared policy.
type ReasoningCapabilities struct {
	Mode    string
	Efforts []string
	Budgets map[string]int
}

// EncodeReasoningIntent produces native top-level request fields without
// clamping effort levels or converting explicit budgets to approximate efforts.
func EncodeReasoningIntent(intent ReasoningIntent, target Protocol, caps ReasoningCapabilities, limit int) (map[string]any, error) {
	if !intent.Present {
		if intent.Disabled || intent.Effort != "" || intent.BudgetTokens != 0 {
			return nil, reasoningError("reasoning", "reasoning intent is inconsistent")
		}
		return nil, nil
	}
	if caps.Mode != "effort" && caps.Mode != "budget" {
		return nil, reasoningError("reasoning", "model does not declare reasoning support")
	}
	if target == AnthropicProtocol && caps.Mode != "budget" || (target == ChatProtocol || target == ResponsesProtocol) && caps.Mode != "effort" {
		return nil, reasoningError("reasoning", "reasoning mode is incompatible with the target protocol")
	}
	if target != ChatProtocol && target != ResponsesProtocol && target != AnthropicProtocol {
		return nil, reasoningError("reasoning", "unsupported target protocol")
	}
	if intent.Disabled && (intent.Effort != "" || intent.BudgetTokens != 0) || intent.Effort != "" && intent.BudgetTokens != 0 {
		return nil, reasoningError("reasoning", "explicit reasoning controls conflict")
	}
	effort := intent.Effort
	if intent.Disabled {
		effort = "none"
	}
	if effort != "" {
		allowed := false
		for _, declared := range caps.Efforts {
			if effort == declared {
				allowed = true
			}
		}
		if !reasoningLevel(effort) || !allowed {
			return nil, reasoningError("reasoning.effort", "model does not support the requested reasoning effort")
		}
	}
	if caps.Mode == "effort" {
		if effort == "" || intent.BudgetTokens != 0 {
			return nil, reasoningError("reasoning", "explicit budgets cannot map to an effort model")
		}
		if target == ChatProtocol {
			return map[string]any{"reasoning_effort": effort}, nil
		}
		return map[string]any{"reasoning": map[string]any{"effort": effort}}, nil
	}
	if effort == "none" {
		return map[string]any{"thinking": map[string]any{"type": "disabled"}}, nil
	}
	budget := intent.BudgetTokens
	if effort != "" {
		budget = caps.Budgets[effort]
	}
	if budget < 1024 || budget >= limit {
		return nil, reasoningError("thinking.budget_tokens", "thinking budget must be at least 1024 and less than the output limit")
	}
	return map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": budget}}, nil
}
