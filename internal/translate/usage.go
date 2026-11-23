package translate

import (
	"encoding/json"
	"math"
)

type usageMerger struct{ values map[string]int64 }

func invalidUsage(field string) error {
	return &ConversionError{Code: "invalid_usage", Stage: "response", Field: field, Description: "invalid token usage"}
}
func (u *usageMerger) merge(protocol Protocol, raw map[string]any) (object, error) {
	if u.values == nil {
		u.values = map[string]int64{}
	}
	fields := map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens", "cache_read_input_tokens": "cache_read_input_tokens", "cache_creation_input_tokens": "cache_creation_input_tokens"}
	if protocol == ChatProtocol {
		fields = map[string]string{"prompt_tokens": "input_tokens", "completion_tokens": "output_tokens"}
	}
	read := func(key string, value any) error {
		n, ok := value.(json.Number)
		if !ok {
			return invalidUsage(key)
		}
		count, err := n.Int64()
		if err != nil || count < 0 {
			return invalidUsage(key)
		}
		if prior, ok := u.values[key]; ok && count < prior {
			return invalidUsage(key)
		}
		u.values[key] = count
		return nil
	}
	for source, target := range fields {
		if value, ok := raw[source]; ok {
			// Anthropic MessageDeltaUsage nullable input/cache counters mean no update.
			if protocol == AnthropicProtocol && value == nil && source != "output_tokens" {
				continue
			}
			if err := read(target, value); err != nil {
				return nil, err
			}
		}
	}
	if protocol == ChatProtocol {
		if details, ok := raw["prompt_tokens_details"]; ok && details != nil {
			d, ok := details.(map[string]any)
			if !ok {
				return nil, invalidUsage("prompt_tokens_details")
			}
			if value, ok := d["cached_tokens"]; ok {
				if err := read("cache_read_input_tokens", value); err != nil {
					return nil, err
				}
			}
		}
	}
	total := u.values["input_tokens"]
	if protocol == AnthropicProtocol {
		for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
			v := u.values[key]
			if v > math.MaxInt64-total {
				return nil, invalidUsage(key)
			}
			total += v
		}
	} else if cached, ok := u.values["cache_read_input_tokens"]; ok {
		if input, known := u.values["input_tokens"]; known && cached > input {
			return nil, invalidUsage("cached_tokens")
		}
	}
	if u.values["output_tokens"] > math.MaxInt64-total {
		return nil, invalidUsage("total_tokens")
	}
	if value, known := raw["total_tokens"]; known && protocol == ChatProtocol {
		n, ok := value.(json.Number)
		if !ok {
			return nil, invalidUsage("total_tokens")
		}
		sum, err := n.Int64()
		if err != nil || sum < 0 {
			return nil, invalidUsage("total_tokens")
		}
		_, inputKnown := u.values["input_tokens"]
		_, outputKnown := u.values["output_tokens"]
		if inputKnown && outputKnown && sum != total+u.values["output_tokens"] {
			return nil, invalidUsage("total_tokens")
		}
	}
	out := object{"input_tokens": total, "output_tokens": u.values["output_tokens"]}
	for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
		if count, known := u.values[key]; known {
			out[key] = count
		}
	}
	if cached, known := u.values["cache_read_input_tokens"]; known {
		out["input_tokens_details"] = object{"cached_tokens": cached}
	}
	return out, nil
}
