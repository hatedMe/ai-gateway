package gateway

import (
	"ai-gateway-go/internal/translate"
	"encoding/json"
	"fmt"
	"strings"
)

type nativeUpstreamKey struct{}
type responseModelKey struct{}
type conversionContextKey struct{}

func protocolForPath(path string) string {
	switch path {
	case "/v1/responses":
		return "openai-responses"
	case "/v1/chat/completions":
		return "openai-compatible"
	default:
		return "anthropic"
	}
}

// Check fields that the legacy preparation would otherwise downgrade before
// the new upstream encoder can detect them. Never inspect arbitrary tool input.
func validateCrossProtocolRequest(raw []byte, target, source string) error {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return fmt.Errorf("invalid JSON request")
	}
	if target != "openai-responses" && source != "openai-responses" {
		allowed := strings.Fields("model messages stream temperature top_p tools tool_choice")
		if source == "openai-compatible" {
			allowed = append(allowed, strings.Fields("max_tokens max_completion_tokens stream_options parallel_tool_calls stop n store")...)
		} else {
			allowed = append(allowed, strings.Fields("max_tokens system stop_sequences")...)
		}
		for key := range root {
			valid := false
			for _, k := range allowed {
				valid = valid || key == k
			}
			if !valid {
				return &translate.ConversionError{Code: "unsupported_field", Stage: "request", Field: key, Description: "unsupported request field"}
			}
		}
		if value, ok := root["store"]; ok && value != false {
			return fmt.Errorf("store is unsupported across these upstream protocols")
		}
		messages, _ := root["messages"].([]any)
		for _, v := range messages {
			msg, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for key, value := range msg {
				valid := key == "role" || key == "content"
				if source == "openai-compatible" {
					valid = valid || key == "tool_calls" || key == "tool_call_id" || (key == "refusal" && value == nil)
				}
				if !valid {
					return &translate.ConversionError{Code: "unsupported_field", Stage: "request", Field: "messages." + key, Description: "unsupported message field"}
				}
			}
		}
	}
	for _, key := range []string{"thinking", "output_config", "reasoning_effort", "reasoning", "include", "cache_control"} {
		if _, ok := root[key]; ok && target != "openai-responses" {
			return &translate.ConversionError{Code: "unsupported_field", Stage: "request", Field: key, Description: "unsupported request field"}
		}
	}
	var checkContent func(any) error
	checkContent = func(content any) error {
		blocks, _ := content.([]any)
		for _, v := range blocks {
			block, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"cache_control", "signature", "reasoning_blocks", "reasoning_content"} {
				if _, ok := block[key]; ok {
					return fmt.Errorf("field %s cannot be replayed across upstream protocols", key)
				}
			}
			switch block["type"] {
			case "thinking", "redacted_thinking":
				return fmt.Errorf("thinking history cannot be replayed across upstream protocols")
			case "tool_result":
				if err := checkContent(block["content"]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	messages, _ := root["messages"].([]any)
	for _, v := range messages {
		msg, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"reasoning_blocks", "reasoning_content"} {
			if _, ok := msg[key]; ok {
				return fmt.Errorf("field %s cannot be replayed across upstream protocols", key)
			}
		}
		if err := checkContent(msg["content"]); err != nil {
			return err
		}
	}
	if err := checkContent(root["system"]); err != nil {
		return err
	}
	tools, _ := root["tools"].([]any)
	for _, v := range tools {
		if tool, ok := v.(map[string]any); ok {
			if _, ok := tool["cache_control"]; ok {
				return fmt.Errorf("tool cache_control is unsupported across upstream protocols")
			}
		}
	}
	return nil
}

func (h *handler) upstreamEndpoint() string {
	base := strings.TrimSuffix(h.endpoint, "/responses")
	switch h.protocol {
	case "anthropic":
		return base + "/messages"
	case "openai-compatible":
		return base + "/chat/completions"
	default:
		return h.endpoint
	}
}

func newMultiUpstream(config Config) (*handler, error) {
	if len(config.Upstreams) == 0 {
		return nil, fmt.Errorf("upstreams must not be empty")
	}
	routes, err := newRouter(config.Upstreams)
	if err != nil {
		return nil, err
	}
	parent := &handler{config: config, routes: routes, upstreams: map[string]*handler{}}
	parent.config.ModelAliases = map[string]string{}
	for name, up := range config.Upstreams {
		if strings.TrimSpace(name) == "" || len(up.Models) == 0 {
			return nil, fmt.Errorf("upstreams require names and models")
		}
		switch up.Protocol {
		case "openai-responses", "openai-compatible", "anthropic":
		default:
			return nil, fmt.Errorf("unsupported upstream protocol")
		}
		if up.Protocol == "anthropic" && strings.TrimSpace(up.AnthropicVersion) == "" {
			return nil, fmt.Errorf("anthropic_version is required")
		}
		childConfig := config
		childConfig.Upstreams = nil
		childConfig.ModelAliases = nil
		childConfig.BaseURL = up.BaseURL
		childConfig.APIKey = up.APIKey
		childConfig.ProxyURL = up.ProxyURL
		childConfig.Model = ""
		childConfig.ResponseHeaderTimeout = up.ResponseHeaderTimeout
		childConfig.ReadIdleTimeout = up.ReadIdleTimeout
		built, err := New(childConfig)
		if err != nil {
			return nil, err
		}
		child := built.(*handler)
		child.protocol = up.Protocol
		child.upstreamName = name
		child.anthropicVersion = up.AnthropicVersion
		parent.upstreams[name] = child
		for _, m := range up.Models {
			if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Model) == "" {
				return nil, fmt.Errorf("model name and model must not be empty")
			}
			parent.config.ModelAliases[m.Name] = m.Name
		}
	}
	return parent, nil
}
