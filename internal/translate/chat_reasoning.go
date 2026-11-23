package translate

import (
	"encoding/json"
	"strings"
)

func emptyChatDelta(delta object) bool {
	for _, field := range []string{"content", "refusal", "reasoning_content"} {
		if text, _ := delta[field].(string); text != "" {
			return false
		}
	}
	if calls, _ := delta["tool_calls"].([]any); len(calls) > 0 {
		return false
	}
	return true
}

// Some Chat providers expose readable reasoning under an alias and repeat it
// in reasoning_details. Normalize only readable text, never opaque replay state.
func normalizeChatReasoning(message object, stage string) error {
	fail := func(field, description string) error {
		return &ConversionError{Code: "unsupported_capability", Stage: stage, Field: field, Description: description}
	}
	reasoning := ""
	merge := func(field string, value any) error {
		if value == nil {
			return nil
		}
		text, ok := value.(string)
		if !ok {
			return fail(field, "readable reasoning must be text")
		}
		if reasoning != "" && text != "" && text != reasoning {
			return fail(field, "conflicting readable reasoning representations")
		}
		if text != "" {
			reasoning = text
		}
		return nil
	}
	if err := merge("reasoning_content", message["reasoning_content"]); err != nil {
		return err
	}
	if err := merge("reasoning", message["reasoning"]); err != nil {
		return err
	}
	if value := message["reasoning_details"]; value != nil {
		details, ok := value.([]any)
		if !ok {
			return fail("reasoning_details", "readable reasoning details must be an array")
		}
		var text strings.Builder
		for _, value := range details {
			detail, ok := value.(map[string]any)
			if !ok || detail["type"] != "reasoning.text" {
				return fail("reasoning_details", "only readable reasoning text details can be converted")
			}
			for key, value := range detail {
				if value == nil {
					continue
				}
				switch key {
				case "type", "text":
				case "format":
					if _, ok := value.(string); !ok {
						return fail("reasoning_details.format", "invalid reasoning format metadata")
					}
				case "index":
					number, ok := value.(json.Number)
					if !ok {
						return fail("reasoning_details.index", "invalid reasoning detail index")
					}
					if n, err := number.Int64(); err != nil || n < 0 {
						return fail("reasoning_details.index", "invalid reasoning detail index")
					}
				default:
					return fail("reasoning_details."+key, "unsupported reasoning detail metadata")
				}
			}
			part, ok := detail["text"].(string)
			if !ok {
				return fail("reasoning_details.text", "readable reasoning detail must contain text")
			}
			text.WriteString(part)
		}
		if err := merge("reasoning_details", text.String()); err != nil {
			return err
		}
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	delete(message, "reasoning")
	delete(message, "reasoning_details")
	return nil
}
