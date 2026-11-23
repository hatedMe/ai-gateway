package translate

import "encoding/json"

func (c ModelCapabilities) allowsImage() bool {
	for _, m := range c.InputModalities {
		if m == "image" {
			return true
		}
	}
	return false
}

// PrepareClientCapabilities validates provider fields before legacy preparation
// can downgrade them. Native requests deliberately bypass this function.
func PrepareClientCapabilities(raw []byte, c *ConversionContext) (body []byte, err error) {
	defer func() { err = conversionError(err, "request") }()
	root, err := rawObject(raw)
	if err != nil {
		return nil, conversionError(err, "request")
	}
	if err = RejectCrossProviderReasoningReplay(raw, c.Source); err != nil {
		return nil, err
	}
	if c.Source != ResponsesProtocol {
		var messages []map[string]json.RawMessage
		if err = json.Unmarshal(root["messages"], &messages); err != nil {
			return nil, conversionError(err, "request")
		}
		for _, message := range messages {
			var role string
			_ = json.Unmarshal(message["role"], &role)
			content := message["content"]
			var blocks []json.RawMessage
			if json.Unmarshal(content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				var item map[string]json.RawMessage
				_ = json.Unmarshal(block, &item)
				var typ string
				_ = json.Unmarshal(item["type"], &typ)
				if typ == "tool_use" {
					continue
				}
				if typ == "tool_result" {
					var result []map[string]json.RawMessage
					if json.Unmarshal(item["content"], &result) == nil {
						for _, part := range result {
							if string(part["type"]) != `"text"` {
								return nil, &ConversionError{Code: "unsupported_capability", Stage: "request", Field: "messages.content.tool_result", Description: "only text tool results can be converted"}
							}
						}
					}
					continue
				}
				if typ == "text" {
					continue
				}
				if role != "user" {
					return nil, &ConversionError{Code: "unsupported_capability", Stage: "request", Field: "messages.content", Description: "only user image content can be converted"}
				}
				one, _ := json.Marshal([]json.RawMessage{block})
				if _, err = ConvertInputContent(one, c.Source, ResponsesProtocol, c.Capabilities.allowsImage()); err != nil {
					return nil, err
				}
			}
		}
	}
	var format json.RawMessage
	switch c.Source {
	case ChatProtocol:
		format = root["response_format"]
		delete(root, "response_format")
	case AnthropicProtocol:
		if value, ok := root["output_config"]; ok {
			out, e := rawObject(value)
			if e != nil {
				return nil, conversionError(e, "request")
			}
			format = out["format"]
			delete(out, "format")
			if len(out) == 0 {
				delete(root, "output_config")
			} else {
				root["output_config"], _ = json.Marshal(out)
			}
		}
	case ResponsesProtocol:
		if value, ok := root["text"]; ok {
			out, e := rawObject(value)
			if e != nil {
				return nil, conversionError(e, "request")
			}
			format = out["format"]
			delete(out, "format")
			if len(out) > 0 {
				return nil, &ConversionError{Code: "unsupported_field", Stage: "request", Field: "text", Description: "unsupported text options"}
			}
			delete(root, "text")
		}
	}
	if len(format) > 0 {
		c.outputFormat, err = ConvertOutputFormat(format, c.Source, c.Target, c.Capabilities.JSONSchema)
		if err != nil {
			return nil, err
		}
	}
	if c.Capabilities.Reasoning != nil {
		if value, ok := root["reasoning"]; ok {
			settings, e := rawObject(value)
			if e != nil {
				return nil, e
			}
			if _, exists := settings["summary"]; exists && c.Target != ResponsesProtocol {
				return nil, &ConversionError{Code: "unsupported_field", Stage: "request", Field: "reasoning.summary", Description: "summary preferences have no equivalent target control"}
			}
		}
		if value, ok := root["thinking"]; ok {
			settings, e := rawObject(value)
			if e != nil {
				return nil, e
			}
			if value, exists := settings["display"]; exists {
				var display string
				if json.Unmarshal(value, &display) != nil || (display != "summarized" && display != "omitted" && display != "updates") {
					return nil, &ConversionError{Code: "unsupported_field", Stage: "request", Field: "thinking.display", Description: "unsupported thinking display"}
				}
				c.omitThinking = display == "omitted" || display == "updates"
			}
		}

		intent, e := ParseReasoningIntent(raw, c.Source)
		if e != nil {
			return nil, e
		}
		c.reasoningIntent = &intent
		delete(root, "reasoning_effort")
		delete(root, "reasoning")
		delete(root, "thinking")
		if value, ok := root["output_config"]; ok {
			out, e := rawObject(value)
			if e != nil {
				return nil, conversionError(e, "request")
			}
			delete(out, "effort")
			if len(out) == 0 {
				delete(root, "output_config")
			} else {
				root["output_config"], _ = json.Marshal(out)
			}
		}
	}
	return json.Marshal(root)
}

func applyAdvancedOutput(body []byte, c *ConversionContext) ([]byte, error) {
	root, err := rawObject(body)
	if err != nil {
		return nil, err
	}
	if len(c.outputFormat) > 0 {
		switch c.Target {
		case ChatProtocol:
			root["response_format"] = c.outputFormat
		case ResponsesProtocol:
			root["text"], _ = json.Marshal(object{"format": json.RawMessage(c.outputFormat)})
		case AnthropicProtocol:
			root["output_config"], _ = json.Marshal(object{"format": json.RawMessage(c.outputFormat)})
		}
	}
	if c.reasoningIntent != nil {
		field := "max_output_tokens"
		if c.Target == AnthropicProtocol {
			field = "max_tokens"
		}
		if c.Target == ChatProtocol {
			field = "max_completion_tokens"
			if c.Capabilities.ChatTokenLimitField != "" {
				field = c.Capabilities.ChatTokenLimitField
			}
		}
		limit := 4096
		if v, ok := root[field]; ok {
			_ = json.Unmarshal(v, &limit)
		}
		mapped, e := EncodeReasoningIntent(*c.reasoningIntent, c.Target, *c.Capabilities.Reasoning, limit)
		if e != nil {
			return nil, e
		}
		for key, value := range mapped {
			root[key], _ = json.Marshal(value)
		}
	}
	return json.Marshal(root)
}
