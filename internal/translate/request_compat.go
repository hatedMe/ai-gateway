package translate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var legalToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// PrepareRequest retains the request context needed for response compatibility.
func PrepareRequest(raw []byte, options RequestOptions) (PreparedRequest, error) {
	p := PreparedRequest{ToolNames: map[string]string{}}
	var root object
	if err := decodeNumbers(raw, &root); err != nil {
		return p, err
	}
	if root == nil {
		return p, fmt.Errorf("expected JSON object")
	}
	warn := func(s string) {
		for _, v := range p.Warnings {
			if v == s {
				return
			}
		}
		p.Warnings = append(p.Warnings, s)
	}
	if err := stripCache(root, warn); err != nil {
		return p, err
	}
	if thinking, ok := root["thinking"].(map[string]any); ok {
		display, _ := thinking["display"].(string)
		p.OmitThinking = display == "omitted" || display == "updates"
		if display == "updates" {
			warn("thinking.display updates maps to omitted; provider-specific progress updates are unavailable")
		}
	}
	if err := prepareContextManagement(root, warn); err != nil {
		return p, err
	}
	if v, ok := root["stop_sequences"]; ok {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, &p.StopSequences); err != nil {
			return p, fmt.Errorf("stop_sequences must be strings")
		}
		for _, s := range p.StopSequences {
			if s == "" {
				return p, fmt.Errorf("stop sequences must not be empty")
			}
		}
		delete(root, "stop_sequences")
	}
	if v, ok := root["top_k"]; ok {
		number, yes := v.(json.Number)
		n, err := number.Int64()
		if !yes || err != nil || n <= 0 {
			return p, fmt.Errorf("top_k must be a positive integer")
		}
		delete(root, "top_k")
		warn("top_k has no Responses equivalent and was omitted")
	}
	extra := object{}
	if v, ok := root["service_tier"]; ok {
		s, yes := v.(string)
		if !yes {
			return p, fmt.Errorf("invalid service_tier")
		}
		switch s {
		case "auto":
			extra["service_tier"] = "auto"
		case "standard_only":
			extra["service_tier"] = "default"
		default:
			return p, fmt.Errorf("unsupported service_tier")
		}
		delete(root, "service_tier")
	}
	if v, ok := root["speed"]; ok {
		if v != "fast" && v != "standard" {
			return p, fmt.Errorf("unsupported speed")
		}
		if v == "fast" {
			extra["service_tier"] = "priority"
			warn("speed fast maps to upstream priority service tier")
		}
		delete(root, "speed")
	}
	reserved := map[string]bool{}
	if tools, ok := root["tools"].([]any); ok {
		for _, v := range tools {
			if t, ok := v.(map[string]any); ok {
				if n, ok := t["name"].(string); ok && legalToolName.MatchString(n) {
					reserved[n] = true
				}
			}
		}
	}
	renamed := map[string]string{}
	nameFor := func(name string) string {
		if n, ok := renamed[name]; ok {
			return n
		}
		n := name
		if !legalToolName.MatchString(name) {
			sum := sha256.Sum256([]byte(name))
			n = fmt.Sprintf("tool_%x", sum[:])[:64]
			for salt := 1; reserved[n]; salt++ {
				sum = sha256.Sum256([]byte(fmt.Sprintf("%s:%d", name, salt)))
				n = fmt.Sprintf("tool_%x", sum[:])[:64]
			}
			reserved[n] = true
			p.ToolNames[n] = name
		}
		renamed[name] = n
		return n
	}
	var builtin []any
	if tools, ok := root["tools"].([]any); ok {
		kept := []any{}
		for _, v := range tools {
			tool, ok := v.(map[string]any)
			if !ok {
				return p, fmt.Errorf("tool must be an object")
			}
			typ, _ := tool["type"].(string)
			switch {
			case strings.HasPrefix(typ, "web_search"), strings.HasPrefix(typ, "web_fetch"):
				web := object{"type": "web_search"}
				if domains, ok := tool["allowed_domains"]; ok {
					arr, yes := domains.([]any)
					if !yes {
						return p, fmt.Errorf("allowed_domains must be an array")
					}
					for _, d := range arr {
						if _, ok := d.(string); !ok {
							return p, fmt.Errorf("allowed_domains must contain strings")
						}
					}
					web["filters"] = object{"allowed_domains": arr}
				}
				for k := range tool {
					if k != "type" && k != "name" && k != "allowed_domains" {
						warn("server tool option " + k + " has no equivalent and was omitted")
					}
				}
				builtin = append(builtin, web)
				if strings.HasPrefix(typ, "web_fetch") {
					warn("web_fetch maps to web_search; exact URL fetch semantics are unavailable")
				}
				continue
			case strings.HasPrefix(typ, "code_execution"):
				for k := range tool {
					if k != "type" && k != "name" {
						warn("server tool option " + k + " has no equivalent and was omitted")
					}
				}
				builtin = append(builtin, object{"type": "code_interpreter", "container": object{"type": "auto"}})
				continue
			case typ == "custom" || typ == "":
				delete(tool, "type")
			case strings.HasPrefix(typ, "bash"), strings.HasPrefix(typ, "text_editor"), strings.HasPrefix(typ, "computer"):
				delete(tool, "type")
				if _, ok := tool["input_schema"]; !ok {
					tool["input_schema"] = clientToolSchema(typ)
				}
				for _, key := range []string{"display_width_px", "display_height_px", "display_number"} {
					if val, ok := tool[key]; ok {
						tool["description"] = fmt.Sprint(tool["description"], " ", key, "=", val)
						delete(tool, key)
					}
				}
			default:
				return p, fmt.Errorf("unsupported tool type %q", typ)
			}
			name, ok := tool["name"].(string)
			if !ok || name == "" {
				return p, fmt.Errorf("tool name is required")
			}
			tool["name"] = nameFor(name)
			if examples, ok := tool["input_examples"]; ok {
				if _, yes := examples.([]any); !yes {
					return p, fmt.Errorf("input_examples must be an array")
				}
				b, _ := json.Marshal(examples)
				desc, _ := tool["description"].(string)
				tool["description"] = desc + "\nExample inputs: " + string(b)
				delete(tool, "input_examples")
			}
			if deferValue, ok := tool["defer_loading"]; ok {
				b, yes := deferValue.(bool)
				if !yes {
					return p, fmt.Errorf("defer_loading must be boolean")
				}
				if b {
					warn("defer_loading is downgraded to immediate tool loading")
				}
				delete(tool, "defer_loading")
			}
			kept = append(kept, tool)
		}
		root["tools"] = kept
	}
	if len(builtin) > 0 {
		if tc, ok := root["tool_choice"].(map[string]any); ok {
			if typ, ok := tc["type"].(string); ok && (typ == "auto" || typ == "any" || typ == "none") {
				for k := range tc {
					if k != "type" && k != "disable_parallel_tool_use" {
						return p, fmt.Errorf("unsupported tool_choice field %q", k)
					}
				}
				choice := typ
				if choice == "any" {
					choice = "required"
				}
				extra["tool_choice"] = choice
				if d, ok := tc["disable_parallel_tool_use"]; ok {
					b, yes := d.(bool)
					if !yes {
						return p, fmt.Errorf("disable_parallel_tool_use must be boolean")
					}
					extra["parallel_tool_calls"] = !b
				}
				delete(root, "tool_choice")
			}
		}
	}
	if tc, ok := root["tool_choice"].(map[string]any); ok {
		if n, ok := tc["name"].(string); ok {
			tc["name"] = nameFor(n)
		}
	}
	if msgs, ok := root["messages"].([]any); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			normalizedContent, err := prepareContentCompatibility(msg["content"], warn)
			if err != nil {
				return p, err
			}
			msg["content"] = normalizedContent
			parts, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			kept := []any{}
			for _, v := range parts {
				b, ok := v.(map[string]any)
				if !ok {
					kept = append(kept, v)
					continue
				}
				typ, _ := b["type"].(string)
				if typ == "tool_use" {
					if n, ok := b["name"].(string); ok {
						b["name"] = nameFor(n)
					}
				}
				if typ == "redacted_thinking" {
					if _, ok := b["data"].(string); !ok {
						return p, fmt.Errorf("redacted_thinking data must be a string")
					}
					warn("external redacted thinking was omitted; Anthropic signatures are not OpenAI encrypted reasoning")
					continue
				}
				if typ == "thinking" {
					sig, _ := b["signature"].(string)
					if !strings.HasPrefix(sig, reasoningSignaturePrefix) {
						if _, ok := b["thinking"].(string); !ok {
							return p, fmt.Errorf("thinking must be a string")
						}
						warn("external thinking was omitted; Anthropic signatures are not OpenAI encrypted reasoning")
						continue
					}
				}
				if typ == "server_tool_use" || strings.HasSuffix(typ, "_tool_result") && typ != "tool_result" {
					encoded, _ := json.Marshal(b)
					b = object{"type": "text", "text": "[Server tool history] " + string(encoded)}
					warn("server tool history is preserved as readable context, not re-executed")
				}
				kept = append(kept, b)
			}
			if len(kept) == 0 && len(parts) > 0 {
				kept = append(kept, object{"type": "text", "text": "[Reasoning history omitted]"})
			}
			msg["content"] = kept
		}
		if len(msgs) > 0 {
			last, _ := msgs[len(msgs)-1].(map[string]any)
			if last["role"] == "assistant" {
				encoded, _ := json.Marshal(last["content"])
				root["system"] = appendSystem(root["system"], "The assistant draft below is a starting point. Continue it where appropriate; exact prefix continuation is not guaranteed. Draft: "+string(encoded))
				root["messages"] = append(msgs, object{"role": "user", "content": "Continue the assistant draft."})
				warn("assistant prefill is approximated with a developer instruction; exact continuation is not guaranteed")
			}
		}
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return p, err
	}
	body, err := convertRequest(normalized, options.Model)
	if err != nil {
		return p, err
	}
	var out object
	_ = decodeNumbers(body, &out)
	for k, v := range extra {
		out[k] = v
	}
	if len(builtin) > 0 {
		tools, _ := out["tools"].([]any)
		out["tools"] = append(tools, builtin...)
	}
	if reasoning, ok := out["reasoning"].(map[string]any); ok && len(options.ReasoningEfforts) > 0 {
		wanted, _ := reasoning["effort"].(string)
		levels := []string{"none", "minimal", "low", "medium", "high", "xhigh"}
		rank := func(s string) int {
			for i, v := range levels {
				if s == v {
					return i
				}
			}
			return -1
		}
		best := ""
		distance := 100
		for _, candidate := range options.ReasoningEfforts {
			r := rank(candidate)
			if r < 0 {
				return p, fmt.Errorf("unknown supported reasoning effort %q", candidate)
			}
			if candidate == wanted {
				best = candidate
				break
			}
			if wanted == "none" || candidate == "none" {
				continue
			}
			d := r - rank(wanted)
			if d < 0 {
				d = -d
			}
			if d < distance || d == distance && r < rank(best) {
				best = candidate
				distance = d
			}
		}
		if best == "" {
			return p, fmt.Errorf("upstream does not support reasoning effort %q", wanted)
		}
		if best != wanted {
			warn("reasoning effort was clamped to upstream supported level " + best)
			reasoning["effort"] = best
		}
	}
	p.Body, err = json.Marshal(out)
	return p, err
}

func ConvertRequest(raw []byte, model string) ([]byte, error) {
	p, err := PrepareRequest(raw, RequestOptions{Model: model})
	return p.Body, err
}

func stripCache(v any, warn func(string)) error {
	switch x := v.(type) {
	case map[string]any:
		if c, ok := x["cache_control"]; ok {
			m, ok := c.(map[string]any)
			if !ok || m["type"] != "ephemeral" {
				return fmt.Errorf("cache_control requires ephemeral type")
			}
			for k, val := range m {
				if k != "type" && k != "ttl" {
					return fmt.Errorf("unsupported cache_control field %q", k)
				}
				if k == "ttl" && val != "5m" && val != "1h" {
					return fmt.Errorf("cache_control ttl must be 5m or 1h")
				}
			}
			delete(x, "cache_control")
			warn("cache_control was removed; upstream automatic caching has different scope and TTL semantics")
		}
		for k, child := range x {
			if k == "input_schema" || k == "schema" || k == "input" || k == "input_examples" {
				continue
			}
			if err := stripCache(child, warn); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := stripCache(child, warn); err != nil {
				return err
			}
		}
	}
	return nil
}
func appendSystem(v any, s string) []any {
	switch x := v.(type) {
	case string:
		return []any{object{"type": "text", "text": x}, object{"type": "text", "text": s}}
	case []any:
		return append(x, object{"type": "text", "text": s})
	default:
		return []any{object{"type": "text", "text": s}}
	}
}
func clientToolSchema(typ string) object {
	props := object{}
	if strings.HasPrefix(typ, "bash") {
		props["command"] = object{"type": "string"}
		props["restart"] = object{"type": "boolean"}
	} else {
		props["command"] = object{"type": "string"}
		if strings.HasPrefix(typ, "computer") {
			props["action"] = object{"type": "string"}
			props["coordinate"] = object{"type": "array", "items": object{"type": "integer"}}
			props["text"] = object{"type": "string"}
		} else {
			for _, k := range []string{"path", "file_text", "old_str", "new_str"} {
				props[k] = object{"type": "string"}
			}
			props["insert_line"] = object{"type": "integer"}
			props["view_range"] = object{"type": "array", "items": object{"type": "integer"}}
		}
	}
	return object{"type": "object", "properties": props}
}

// Walk content containers only: tool inputs and JSON Schemas are instance data.
func prepareContentCompatibility(value any, warn func(string)) (any, error) {
	parts, ok := value.([]any)
	if !ok {
		return value, nil
	}
	out := []any{}
	for _, value := range parts {
		block, ok := value.(map[string]any)
		if !ok {
			out = append(out, value)
			continue
		}
		typ, _ := block["type"].(string)
		if _, ok := block["citations"]; ok {
			warn("citation history and source text are retained as context; upstream citation behavior may differ")
		}
		if typ == "document" {
			if context, ok := block["context"]; ok {
				s, ok := context.(string)
				if !ok {
					return nil, fmt.Errorf("document context must be a string")
				}
				title, _ := block["title"].(string)
				out = append(out, object{"type": "text", "text": "[Document context: " + title + "] " + s})
				delete(block, "context")
			}
		}
		if typ == "tool_result" || typ == "search_result" {
			if content, ok := block["content"]; ok {
				converted, err := prepareContentCompatibility(content, warn)
				if err != nil {
					return nil, err
				}
				block["content"] = converted
			}
		}
		if typ == "document" {
			if source, ok := block["source"].(map[string]any); ok && source["type"] == "content" {
				converted, err := prepareContentCompatibility(source["content"], warn)
				if err != nil {
					return nil, err
				}
				source["content"] = converted
			}
		}
		out = append(out, block)
	}
	return out, nil
}
