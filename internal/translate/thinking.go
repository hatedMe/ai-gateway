package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const reasoningSignaturePrefix = "openai-reasoning-v1:"

// This gateway carrier is not an Anthropic signature. It preserves the complete
// OpenAI reasoning item across a stateless Messages tool round trip.
type reasoningCarrier struct {
	ID               string         `json:"id,omitempty"`
	Type             string         `json:"type"`
	Summary          []upstreamPart `json:"summary"`
	Content          []upstreamPart `json:"content,omitempty"`
	EncryptedContent string         `json:"encrypted_content,omitempty"`
	Omitted          bool           `json:"omitted,omitempty"`
}

func (item upstreamItem) thinkingBlock() (object, error) {
	if len(item.Summary) == 0 && len(item.Content) == 0 && item.EncryptedContent == "" {
		return nil, nil
	}
	summary := make([]upstreamPart, 0, len(item.Summary))
	texts := make([]string, 0, len(item.Summary))
	for _, p := range item.Summary {
		if p.Type != "summary_text" {
			return nil, fmt.Errorf("unsupported reasoning summary %q", p.Type)
		}
		summary = append(summary, upstreamPart{Type: p.Type, Text: p.Text})
		texts = append(texts, p.Text)
	}
	if len(texts) == 0 {
		for _, p := range item.Content {
			if p.Type != "reasoning_text" {
				return nil, fmt.Errorf("unsupported reasoning content %q", p.Type)
			}
			texts = append(texts, p.Text)
		}
	}
	raw, err := json.Marshal(reasoningCarrier{ID: item.ID, Type: "reasoning", Summary: summary, Content: item.Content, EncryptedContent: item.EncryptedContent})
	if err != nil {
		return nil, err
	}
	return object{"type": "thinking", "thinking": strings.Join(texts, "\n\n"), "signature": reasoningSignaturePrefix + base64.RawURLEncoding.EncodeToString(raw)}, nil
}

func reasoningHistory(block map[string]json.RawMessage) (object, error) {
	if err := only(block, "type", "thinking", "signature"); err != nil {
		return nil, err
	}
	thinking, err := text(block["thinking"])
	if err != nil {
		return nil, err
	}
	signature, err := text(block["signature"])
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(signature, reasoningSignaturePrefix) {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(signature, reasoningSignaturePrefix))
	if err != nil {
		return nil, fmt.Errorf("invalid thinking signature encoding")
	}
	fields, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	if err = only(fields, "id", "type", "summary", "content", "encrypted_content", "omitted"); err != nil {
		return nil, err
	}
	var carrier reasoningCarrier
	if err = json.Unmarshal(raw, &carrier); err != nil {
		return nil, err
	}
	if carrier.Type != "reasoning" || carrier.Summary == nil {
		return nil, fmt.Errorf("invalid thinking signature payload")
	}
	texts := make([]string, 0, len(carrier.Summary))
	for _, p := range carrier.Summary {
		if p.Type != "summary_text" {
			return nil, fmt.Errorf("invalid thinking signature summary")
		}
		texts = append(texts, p.Text)
	}
	if len(texts) == 0 {
		for _, p := range carrier.Content {
			if p.Type != "reasoning_text" {
				return nil, fmt.Errorf("invalid reasoning content")
			}
			texts = append(texts, p.Text)
		}
	}
	expected := strings.Join(texts, "\n\n")
	if carrier.Omitted {
		expected = ""
	}
	if thinking != expected {
		return nil, fmt.Errorf("thinking content differs from its signature carrier")
	}
	// Summaries alone cannot restore hidden reasoning. Do not fabricate an
	// encrypted state or inject the summary as assistant answer text.
	if carrier.EncryptedContent == "" && len(carrier.Content) == 0 {
		return nil, nil
	}
	item := object{"type": "reasoning", "summary": carrier.Summary, "encrypted_content": carrier.EncryptedContent}
	if len(carrier.Content) > 0 {
		item["content"] = carrier.Content
	}
	if carrier.ID != "" {
		item["id"] = carrier.ID
	}
	return item, nil
}

func thinkingConfig(root map[string]json.RawMessage, maxTokens int, out object) error {
	effort := ""
	display := ""
	typ := ""
	if raw, ok := root["thinking"]; ok {
		config, err := rawObject(raw)
		if err != nil {
			return err
		}
		if err = only(config, "type", "budget_tokens", "display"); err != nil {
			return err
		}
		if raw, ok := config["display"]; ok {
			display, err = text(raw)
			if err != nil {
				return fmt.Errorf("thinking.display must be a string")
			}
			if display != "summarized" && display != "omitted" && display != "updates" {
				return fmt.Errorf("unsupported thinking.display %q", display)
			}
		}
		typ, err = text(config["type"])
		if err != nil {
			return err
		}
		switch typ {
		case "enabled":
			var budget int
			if err = json.Unmarshal(config["budget_tokens"], &budget); err != nil {
				return fmt.Errorf("thinking.budget_tokens must be an integer")
			}
			if budget < 1024 || budget >= maxTokens {
				return fmt.Errorf("thinking budget must be at least 1024 and less than max_tokens")
			}
			switch {
			case budget <= 1024:
				effort = "low"
			case budget <= 8192:
				effort = "medium"
			case budget <= 24576:
				effort = "high"
			default:
				effort = "xhigh"
			}
		case "adaptive":
			effort = "high"
		case "disabled":
			effort = "none"
		default:
			return fmt.Errorf("unsupported thinking type %q", typ)
		}
		if typ != "enabled" {
			if _, ok := config["budget_tokens"]; ok {
				return fmt.Errorf("budget_tokens requires enabled thinking")
			}
		}
	}
	if raw, ok := root["output_config"]; ok {
		config, err := rawObject(raw)
		if err != nil {
			return err
		}
		if err = only(config, "effort", "format"); err != nil {
			return err
		}
		if r, ok := config["effort"]; ok {
			if typ == "disabled" {
				return fmt.Errorf("effort conflicts with disabled thinking")
			}
			effort, err = text(r)
			if err != nil {
				return err
			}
			switch effort {
			case "minimal", "low", "medium", "high", "xhigh":
			case "max":
				effort = "xhigh"
			default:
				return fmt.Errorf("unsupported effort %q", effort)
			}
		}
		if r, ok := config["format"]; ok {
			format, err := rawObject(r)
			if err != nil {
				return err
			}
			if err = only(format, "type", "schema"); err != nil {
				return err
			}
			ft, err := text(format["type"])
			if err != nil || ft != "json_schema" {
				return fmt.Errorf("output format must be json_schema")
			}
			schema, err := rawObject(format["schema"])
			if err != nil {
				return err
			}
			if err = validateStrictSchema(schema); err != nil {
				return fmt.Errorf("output schema: %w", err)
			}
			out["text"] = object{"format": object{"type": "json_schema", "name": "response", "schema": schema, "strict": true}}
		}
	}
	if effort != "" {
		reasoning := object{"effort": effort}
		if effort != "none" && display != "omitted" && display != "updates" {
			reasoning["summary"] = "auto"
		}
		out["reasoning"] = reasoning
		out["include"] = []string{"reasoning.encrypted_content"}
	}
	return nil
}

// OmitThinkingSignature preserves the complete reasoning state while recording
// that the client received no visible summary, so empty-text history can replay.
func OmitThinkingSignature(signature string) (string, error) {
	if !strings.HasPrefix(signature, reasoningSignaturePrefix) {
		return "", fmt.Errorf("invalid thinking signature prefix")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(signature, reasoningSignaturePrefix))
	if err != nil {
		return "", err
	}
	var carrier reasoningCarrier
	if err = json.Unmarshal(raw, &carrier); err != nil {
		return "", err
	}
	carrier.Omitted = true
	raw, err = json.Marshal(carrier)
	if err != nil {
		return "", err
	}
	return reasoningSignaturePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}
