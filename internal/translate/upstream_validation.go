package translate

func validateChatContent(m object, stage string) error {
	if err := normalizeChatReasoning(m, stage); err != nil {
		return err
	}
	for key, value := range m {
		if value == nil {
			continue
		}
		switch key {
		case "content", "refusal", "reasoning_content":
			if _, ok := value.(string); !ok {
				return &ConversionError{Code: "unsupported_capability", Stage: stage, Field: key, Description: "unsupported upstream content representation"}
			}
		case "tool_calls":
			if _, ok := value.([]any); !ok {
				return &ConversionError{Code: "invalid_tool_identity", Stage: stage, Field: key, Description: "tool calls must be an array"}
			}
		case "role":
			if value != "assistant" {
				return &ConversionError{Code: "unsupported_capability", Stage: stage, Field: key, Description: "unsupported upstream role"}
			}
		default:
			return &ConversionError{Code: "unsupported_field", Stage: stage, Field: key, Description: "unsupported upstream content field"}
		}
	}
	return nil
}

func validateOutputTools(output []object, stage string) error {
	seen := map[string]bool{}
	for _, item := range output {
		if item["type"] != "function_call" {
			continue
		}
		id, idOK := item["call_id"].(string)
		name, nameOK := item["name"].(string)
		if !idOK || !nameOK || id == "" || name == "" || seen[id] {
			return &ConversionError{Code: "invalid_tool_identity", Stage: stage, Field: "call_id", Description: "invalid or duplicate tool identity"}
		}
		seen[id] = true
		args, ok := item["arguments"].(string)
		if !ok {
			return &ConversionError{Code: "invalid_tool_arguments", Stage: stage, Field: "arguments", Description: "tool arguments must be a JSON object"}
		}
		if _, err := rawObject([]byte(args)); err != nil {
			return &ConversionError{Code: "invalid_tool_arguments", Stage: stage, Field: "arguments", Description: "tool arguments must be a JSON object"}
		}
	}
	return nil
}
