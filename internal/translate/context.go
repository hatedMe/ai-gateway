package translate

import (
	"encoding/json"
	"io"
)

// ModelCapabilities is a routed snapshot; an empty token field is unspecified.
type ModelCapabilities struct {
	ChatTokenLimitField string
	InputModalities     []string
	JSONSchema          bool
	Reasoning           *ReasoningCapabilities
}

type ConversionContext struct {
	Source, Target             Protocol
	ClientModel, UpstreamModel string
	Capabilities               ModelCapabilities
	Prepared                   PreparedRequest
	outputFormat               []byte
	reasoningIntent            *ReasoningIntent
	omitThinking               bool
}

func EncodeUpstreamRequestWithContext(raw []byte, c *ConversionContext) (body []byte, err error) {
	defer func() { err = conversionError(err, "request") }()
	field := c.Capabilities.ChatTokenLimitField
	if c.Source == c.Target {
		if c.Target != ChatProtocol || field == "" {
			return raw, nil
		}
		root, err := rawObject(raw)
		if err != nil {
			return nil, err
		}
		limit, ok := root["max_completion_tokens"]
		if !ok {
			limit, ok = root["max_tokens"]
		}
		delete(root, "max_tokens")
		delete(root, "max_completion_tokens")
		if ok {
			root[field] = limit
		}
		return json.Marshal(root)
	}
	body, err = encodeUpstreamRequest(c.Target, raw, c.Capabilities)
	if err != nil || c.Target != ChatProtocol || field == "" || field == "max_completion_tokens" {
		if err != nil {
			return nil, err
		}
		return applyAdvancedOutput(body, c)
	}
	root, err := rawObject(body)
	if err != nil {
		return nil, err
	}
	root[field] = root["max_completion_tokens"]
	delete(root, "max_completion_tokens")
	body, err = json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return applyAdvancedOutput(body, c)
}

func DecodeUpstreamResponseWithContext(raw []byte, c *ConversionContext) ([]byte, error) {
	body, err := DecodeUpstreamResponse(c.Target, raw)
	if err != nil {
		return nil, conversionError(err, "response")
	}
	if c.Source == AnthropicProtocol && c.Target == ChatProtocol {
		var root object
		_ = decodeNumbers(body, &root)
		if items, ok := root["output"].([]any); ok {
			visible := make([]any, 0, len(items))
			for _, v := range items {
				item, _ := v.(map[string]any)
				if item["type"] == "reasoning" && c.omitThinking {
					continue
				}
				visible = append(visible, item)
				if item["type"] == "reasoning" {
					parts, _ := item["summary"].([]any)
					text := ""
					for _, part := range parts {
						p, _ := part.(map[string]any)
						value, _ := p["text"].(string)
						text += value
					}
					delete(item, "summary")
					item["type"] = "message"
					item["role"] = "assistant"
					item["content"] = []object{{"type": "output_text", "text": text, "annotations": []object{}}}
				}
			}
			root["output"] = visible
		}
		body, _ = json.Marshal(root)
	}
	if c.ClientModel != "" {
		root, err := rawObject(body)
		if err != nil {
			return nil, conversionError(err, "response")
		}
		root["model"], _ = json.Marshal(c.ClientModel)
		return json.Marshal(root)
	}
	return body, nil
}
func DecodeUpstreamStreamWithContext(src io.Reader, c *ConversionContext, emit func(string, any) error) error {
	return conversionError(decodeUpstreamStream(c.Target, src, func(name string, payload any) error {
		if event, ok := payload.(map[string]any); ok && c.ClientModel != "" {
			if response, ok := event["response"].(map[string]any); ok {
				response["model"] = c.ClientModel
			}
		}
		return emit(name, payload)
	}, c.Source == AnthropicProtocol && c.Target == ChatProtocol, c.omitThinking), "stream")
}

// CompletePrepared stores the validated small compatibility state for this request.
func (c *ConversionContext) CompletePrepared(p PreparedRequest) PreparedRequest {
	p.OmitThinking = p.OmitThinking || c.omitThinking
	c.Prepared = p
	return p
}

func (c ModelCapabilities) Snapshot() ModelCapabilities {
	c.InputModalities = append([]string(nil), c.InputModalities...)
	if c.Reasoning != nil {
		r := *c.Reasoning
		r.Efforts = append([]string(nil), r.Efforts...)
		if r.Budgets != nil {
			r.Budgets = map[string]int{}
			for effort, budget := range c.Reasoning.Budgets {
				r.Budgets[effort] = budget
			}
		}
		c.Reasoning = &r
	}
	return c
}
