package translate

import (
	"encoding/json"
	"fmt"
)

type toolIdentity struct{ id, name string }
type streamOperation struct {
	key         int
	value, kind string
}
type chatSourceState struct {
	tools        map[int]*toolIdentity
	pending      []streamOperation
	pendingBytes int
	segment      int
	kind         string
}

func (c *chatSourceState) content(s *normalizedStream, value, kind string) error {
	if kind == "reasoning" && s.omitSummary {
		s.bytes += len(value)
		if s.bytes+c.pendingBytes > 32<<20 {
			return fmt.Errorf("upstream stream content exceeds limit")
		}
		return nil
	}
	if value == "" {
		return nil
	}
	if kind == "reasoning" && s.summaryAsText {
		kind = "summary_text"
	}
	if c.kind != kind {
		c.segment--
		c.kind = kind
	}
	return c.enqueue(s, streamOperation{c.segment, value, kind})
}
func (c *chatSourceState) enqueue(s *normalizedStream, op streamOperation) error {
	c.pendingBytes += len(op.value)
	if c.pendingBytes+s.bytes > 32<<20 || len(c.pending) >= 32768 {
		return fmt.Errorf("upstream stream content exceeds limit")
	}
	c.pending = append(c.pending, op)
	return c.flush(s)
}
func (c *chatSourceState) flush(s *normalizedStream) error {
	for len(c.pending) > 0 {
		op := c.pending[0]
		if _, ok := s.keys[op.key]; !ok {
			var item object
			if op.kind == "reasoning" {
				item = object{"id": fmt.Sprintf("rs_%s_%d", s.id, -op.key), "type": "reasoning", "summary": []object{{"type": "summary_text", "text": ""}}}
			} else if op.kind == "tool" {
				identity := c.tools[op.key]
				if identity.id == "" || identity.name == "" {
					return nil
				}
				item = object{"id": "fc_" + identity.id, "type": "function_call", "call_id": identity.id, "name": identity.name, "arguments": ""}
			} else {
				part := object{"type": "output_text", "text": ""}
				if op.kind == "refusal" {
					part = object{"type": "refusal", "refusal": ""}
				}
				item = object{"id": fmt.Sprintf("msg_%s_%d", s.id, -op.key), "type": "message", "role": "assistant", "content": []object{part}}
			}
			identity, _ := item["id"].(string)
			if len(identity) > (32<<20)-s.bytes-c.pendingBytes {
				return fmt.Errorf("upstream stream identity and pending content limit exceeded")
			}
			if _, err := s.add(op.key, item); err != nil {
				return err
			}
		}
		if err := s.delta(op.key, op.value, op.kind == "tool"); err != nil {
			return err
		}
		c.pendingBytes -= len(op.value)
		c.pending[0] = streamOperation{}
		c.pending = c.pending[1:]
	}
	return nil
}
func (c *chatSourceState) tool(s *normalizedStream, call map[string]any) error {
	n, ok := call["index"].(json.Number)
	if !ok {
		return fmt.Errorf("missing tool index")
	}
	key, err := n.Int64()
	if err != nil || key < 0 || key > 1024 {
		return fmt.Errorf("invalid tool index")
	}
	fn := map[string]any{}
	if value, exists := call["function"]; exists {
		var ok bool
		fn, ok = value.(map[string]any)
		if !ok {
			return &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Field: "function", Description: "invalid tool function"}
		}
	}
	if value, exists := call["type"]; exists && value != "function" {
		return &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Field: "type", Description: "unsupported tool type"}
	}
	if c.tools == nil {
		c.tools = map[int]*toolIdentity{}
	}
	identity := c.tools[int(key)]
	first := identity == nil
	if first {
		identity = &toolIdentity{}
		c.tools[int(key)] = identity
	}
	for _, entry := range []struct {
		source map[string]any
		field  string
		dest   *string
	}{{call, "id", &identity.id}, {fn, "name", &identity.name}} {
		if value, exists := entry.source[entry.field]; exists {
			text, ok := value.(string)
			if !ok {
				return &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Field: entry.field, Description: "invalid tool identity"}
			}
			if text != "" {
				if *entry.dest != "" && *entry.dest != text {
					return &ConversionError{Code: "invalid_tool_identity", Stage: "stream", Field: entry.field, Description: "conflicting tool identity"}
				}
				if *entry.dest == "" {
					s.bytes += len(text)
					if s.bytes+c.pendingBytes > 32<<20 {
						return fmt.Errorf("upstream stream content exceeds limit")
					}
				}
				*entry.dest = text
			}
		}
	}
	c.kind = "tool"
	args, valid := fn["arguments"].(string)
	if _, exists := fn["arguments"]; exists && !valid {
		return &ConversionError{Code: "invalid_tool_arguments", Stage: "stream", Field: "arguments", Description: "tool arguments delta must be text"}
	}
	if first || args != "" {
		return c.enqueue(s, streamOperation{int(key), args, "tool"})
	}
	return c.flush(s)
}
