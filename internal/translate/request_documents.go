package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Strict mode requires a closed object with every property required. Optional
// properties are represented by a nullable value, preserving their optionality.
func validateStrictSchema(schema map[string]json.RawMessage) error {
	var v object
	b, _ := json.Marshal(schema)
	if err := decodeNumbers(b, &v); err != nil {
		return err
	}
	if v["type"] != "object" {
		return fmt.Errorf("strict input_schema must have object type")
	}
	if err := strictNode(v); err != nil {
		return err
	}
	b, _ = json.Marshal(v)
	return json.Unmarshal(b, &schema)
}

// strictNode only visits JSON Schema positions, never arbitrary instance data.
func strictNode(v any) error {
	x, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("schema must be an object")
	}
	objectType := x["type"] == "object"
	if types, ok := x["type"].([]any); ok {
		for _, typ := range types {
			if typ == "object" {
				objectType = true
			}
		}
	}
	if objectType {
		if a, ok := x["additionalProperties"]; ok && a != false {
			return fmt.Errorf("strict schema cannot allow additionalProperties")
		}
		x["additionalProperties"] = false
		props := map[string]any{}
		if p, ok := x["properties"]; ok {
			var yes bool
			props, yes = p.(map[string]any)
			if !yes {
				return fmt.Errorf("schema properties must be an object")
			}
		}
		required := map[string]bool{}
		if value, ok := x["required"]; ok {
			arr, yes := value.([]any)
			if !yes {
				return fmt.Errorf("schema required must be an array")
			}
			for _, r := range arr {
				s, ok := r.(string)
				if !ok {
					return fmt.Errorf("schema required must contain strings")
				}
				if _, ok := props[s]; !ok {
					return fmt.Errorf("schema required names an unknown property")
				}
				required[s] = true
			}
		}
		keys := []string{}
		for k, p := range props {
			keys = append(keys, k)
			if !required[k] {
				m, ok := p.(map[string]any)
				if !ok {
					return fmt.Errorf("schema property must be an object")
				}
				props[k] = object{"anyOf": []any{m, object{"type": "null"}}}
			}
		}
		sort.Strings(keys)
		x["required"] = keys
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if raw, ok := x[key]; ok {
			children, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("schema %s must be an object", key)
			}
			for _, child := range children {
				if err := strictNode(child); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if raw, ok := x[key]; ok {
			children, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("schema %s must be an array", key)
			}
			for _, child := range children {
				if err := strictNode(child); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"items", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedProperties", "additionalItems", "unevaluatedItems", "contentSchema"} {
		if child, ok := x[key]; ok {
			if _, boolean := child.(bool); boolean {
				continue
			}
			if err := strictNode(child); err != nil {
				return err
			}
		}
	}
	return nil
}
func documentPart(b map[string]json.RawMessage, role string) (object, error) {
	typ, _ := text(b["type"])
	if role != "user" {
		return nil, fmt.Errorf("documents require user role")
	}
	if typ == "search_result" {
		if err := only(b, "type", "source", "title", "content", "citations"); err != nil {
			return nil, err
		}
		source, e := text(b["source"])
		if e != nil {
			return nil, e
		}
		title, e := text(b["title"])
		if e != nil {
			return nil, e
		}
		parts, e := contentParts(b["content"])
		if e != nil {
			return nil, e
		}
		texts := []string{"[Search result] " + title + "\nSource: " + source}
		for _, p := range parts {
			block, e := rawObject(p)
			if e != nil {
				return nil, e
			}
			part, e := inputPart(block, "user")
			if e != nil {
				return nil, e
			}
			s, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("search_result content must be text")
			}
			texts = append(texts, s)
		}
		return object{"type": "input_text", "text": strings.Join(texts, "\n")}, nil
	}
	if err := only(b, "type", "source", "title", "context", "citations"); err != nil {
		return nil, err
	}
	src, e := rawObject(b["source"])
	if e != nil {
		return nil, e
	}
	st, e := text(src["type"])
	if e != nil {
		return nil, e
	}
	title := "document.pdf"
	if r, ok := b["title"]; ok {
		title, e = text(r)
		if e != nil {
			return nil, e
		}
	}
	switch st {
	case "base64":
		if e = only(src, "type", "media_type", "data"); e != nil {
			return nil, e
		}
		media, e := text(src["media_type"])
		if e != nil || media != "application/pdf" {
			return nil, fmt.Errorf("base64 document requires application/pdf")
		}
		data, e := text(src["data"])
		if e != nil {
			return nil, e
		}
		if decoded, e := base64.StdEncoding.DecodeString(data); e != nil || len(decoded) == 0 {
			return nil, fmt.Errorf("invalid base64 PDF")
		}
		return object{"type": "input_file", "filename": title, "file_data": "data:application/pdf;base64," + data}, nil
	case "url":
		if e = only(src, "type", "url"); e != nil {
			return nil, e
		}
		u, e := text(src["url"])
		if e != nil {
			return nil, e
		}
		parsed, e := url.Parse(u)
		if e != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("invalid document URL")
		}
		return object{"type": "input_file", "file_url": u}, nil
	case "file", "file_id":
		if e = only(src, "type", "file_id"); e != nil {
			return nil, e
		}
		id, e := text(src["file_id"])
		if e != nil || id == "" {
			return nil, fmt.Errorf("file_id is required")
		}
		return object{"type": "input_file", "file_id": id}, nil
	case "content":
		if e = only(src, "type", "content"); e != nil {
			return nil, e
		}
		parts, e := contentParts(src["content"])
		if e != nil {
			return nil, e
		}
		texts := []string{"[Document: " + title + "]"}
		for _, p := range parts {
			block, e := rawObject(p)
			if e != nil {
				return nil, e
			}
			bt, _ := text(block["type"])
			if bt != "text" {
				return nil, fmt.Errorf("content document supports text blocks only")
			}
			part, e := inputPart(block, "user")
			if e != nil {
				return nil, e
			}
			texts = append(texts, part["text"].(string))
		}
		return object{"type": "input_text", "text": strings.Join(texts, "\n")}, nil
	case "text":
		if e = only(src, "type", "media_type", "data"); e != nil {
			return nil, e
		}
		if media, ok := src["media_type"]; ok {
			s, e := text(media)
			if e != nil || s != "text/plain" {
				return nil, fmt.Errorf("text document requires text/plain")
			}
		}
		data, e := text(src["data"])
		if e != nil {
			return nil, e
		}
		return object{"type": "input_text", "text": "[Document: " + title + "]\n" + data}, nil
	default:
		return nil, fmt.Errorf("unsupported document source %q", st)
	}
}
