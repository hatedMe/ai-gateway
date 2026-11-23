package translate

import "encoding/json"

// ReasoningIntent keeps exact budgets distinct from effort levels. A zero value
// means that the client did not request a reasoning policy.
type ReasoningIntent struct {
	Present      bool
	Disabled     bool
	Effort       string
	BudgetTokens int
}

func reasoningError(field, description string) error {
	return &ConversionError{Code: "unsupported_capability", Stage: "request", Field: field, Description: description}
}

func reasoningLevel(level string) bool {
	switch level {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return true
	}
	return false
}

// ParseReasoningIntent reads native request controls without reading configuration
// or guessing an effort level from a token budget.
func ParseReasoningIntent(raw []byte, source Protocol) (ReasoningIntent, error) {
	var intent ReasoningIntent
	root, err := rawObject(raw)
	if err != nil {
		return intent, reasoningError("reasoning", "reasoning request must be a JSON object")
	}
	var effortRaw json.RawMessage
	field := "reasoning_effort"
	for _, name := range []string{"reasoning_effort", "reasoning", "thinking"} {
		_, present := root[name]
		native := source == ChatProtocol && name == "reasoning_effort" || source == ResponsesProtocol && name == "reasoning" || source == AnthropicProtocol && name == "thinking"
		if present && !native {
			return intent, reasoningError(name, "reasoning controls conflict with the source protocol")
		}
	}
	switch source {
	case ChatProtocol:
		effortRaw = root["reasoning_effort"]
	case ResponsesProtocol:
		field = "reasoning.effort"
		if raw, ok := root["reasoning"]; ok {
			config, err := rawObject(raw)
			if err != nil {
				return intent, reasoningError("reasoning", "reasoning must be an object")
			}
			if err := only(config, "effort", "summary"); err != nil {
				return intent, reasoningError("reasoning", "unsupported reasoning control")
			}
			effortRaw = config["effort"]
		}
	case AnthropicProtocol:
		field = "output_config.effort"
		if raw, ok := root["thinking"]; ok {
			config, err := rawObject(raw)
			if err != nil {
				return intent, reasoningError("thinking", "thinking must be an object")
			}
			if err := only(config, "type", "budget_tokens", "display"); err != nil {
				return intent, reasoningError("thinking", "unsupported thinking control")
			}
			var kind string
			if err := json.Unmarshal(config["type"], &kind); err != nil {
				return intent, reasoningError("thinking.type", "thinking type must be enabled or disabled")
			}
			intent.Present = true
			switch kind {
			case "enabled":
				if err := json.Unmarshal(config["budget_tokens"], &intent.BudgetTokens); err != nil || intent.BudgetTokens < 1024 {
					return ReasoningIntent{}, reasoningError("thinking.budget_tokens", "thinking budget must be an integer of at least 1024")
				}
				if limitRaw, ok := root["max_tokens"]; ok {
					var limit int
					if json.Unmarshal(limitRaw, &limit) != nil || intent.BudgetTokens >= limit {
						return ReasoningIntent{}, reasoningError("thinking.budget_tokens", "thinking budget must be less than the output limit")
					}
				}
			case "disabled":
				intent.Disabled = true
				if _, ok := config["budget_tokens"]; ok {
					return ReasoningIntent{}, reasoningError("thinking.budget_tokens", "disabled thinking conflicts with a budget")
				}
			default:
				return ReasoningIntent{}, reasoningError("thinking.type", "thinking type must be enabled or disabled")
			}
		}
		if raw, ok := root["output_config"]; ok {
			config, err := rawObject(raw)
			if err != nil {
				return ReasoningIntent{}, reasoningError("output_config", "output configuration must be an object")
			}
			effortRaw = config["effort"]
		}
	default:
		return intent, reasoningError("reasoning", "unsupported source protocol")
	}
	if len(effortRaw) > 0 {
		if intent.Present {
			return ReasoningIntent{}, reasoningError(field, "explicit effort conflicts with thinking controls")
		}
		var effort string
		if json.Unmarshal(effortRaw, &effort) != nil || !reasoningLevel(effort) {
			return ReasoningIntent{}, reasoningError(field, "unsupported reasoning effort")
		}
		intent = ReasoningIntent{Present: true, Disabled: effort == "none"}
		if !intent.Disabled {
			intent.Effort = effort
		}
	}
	return intent, nil
}

// RejectCrossProviderReasoningReplay checks only protocol-owned history slots.
// Tool schemas, arguments, inputs and results are opaque business data.
// Callers retain the existing, explicitly supported legacy carrier path.
func RejectCrossProviderReasoningReplay(raw []byte, source Protocol) error {
	root, err := rawObject(raw)
	if err != nil {
		return reasoningError("reasoning", "reasoning request must be a JSON object")
	}
	if source != ResponsesProtocol && source != ChatProtocol && source != AnthropicProtocol {
		return reasoningError("reasoning", "unsupported source protocol")
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if _, ok := root[field]; ok {
			return reasoningError(field, "server reasoning history cannot cross providers")
		}
	}
	historyField := "messages"
	if source == ResponsesProtocol {
		historyField = "input"
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(root[historyField], &items) != nil {
		return nil // Request shape validation belongs to the source parser.
	}
	for _, item := range items {
		if err := rejectReasoningHistoryItem(item, historyField); err != nil {
			return err
		}
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		if kind != "" && kind != "message" || role == "tool" {
			continue
		}
		var content []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &content) == nil {
			for _, block := range content {
				if err := rejectReasoningHistoryItem(block, historyField+".content"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func rejectReasoningHistoryItem(item map[string]json.RawMessage, field string) error {
	var kind string
	_ = json.Unmarshal(item["type"], &kind)
	// Do not interpret business fields on tool items or tool result bodies.
	if kind == "function_call" || kind == "function_call_output" || kind == "tool_use" || kind == "tool_result" {
		return nil
	}
	for _, key := range []string{"signature", "encrypted_content", "reasoning_signature", "reasoning_opaque"} {
		if _, ok := item[key]; ok {
			return reasoningError(field+"."+key, "native reasoning state cannot cross providers")
		}
	}
	if kind == "redacted_thinking" || kind == "thinking" {
		return reasoningError(field, "native thinking history cannot cross providers")
	}
	if kind == "reasoning" {
		if _, ok := item["id"]; ok {
			return reasoningError(field+".id", "server reasoning identity cannot cross providers")
		}
	}
	return nil
}
