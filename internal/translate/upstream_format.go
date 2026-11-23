package translate

import (
	"bytes"
	"encoding/json"
)

// ConvertOutputFormat maps a native format object, not the containing request.
// It accepts a conservative common JSON Schema subset and never repairs schemas.
// Native requests bypass this cross-protocol capability gate at the call site.
func ConvertOutputFormat(raw []byte, source, target Protocol, supported bool) ([]byte, error) {
	fail := func(field string) ([]byte, error) {
		return nil, &ConversionError{Code: "unsupported_capability", Stage: "encode_request", Field: field, Description: "output format cannot be represented equivalently"}
	}
	if !supported {
		return fail("json_schema")
	}
	validProtocol := func(p Protocol) bool { return p == ChatProtocol || p == ResponsesProtocol || p == AnthropicProtocol }
	if !validProtocol(source) || !validProtocol(target) {
		return fail("protocol")
	}
	if !json.Valid(raw) || !uniqueFormatKeys(json.NewDecoder(bytes.NewReader(raw))) {
		return fail("format")
	}
	f, err := rawObject(raw)
	if err != nil {
		return fail("format")
	}
	var typ string
	if json.Unmarshal(f["type"], &typ) != nil || typ != "json_schema" {
		return fail("format.type")
	}
	if source == ChatProtocol {
		if only(f, "type", "json_schema") != nil {
			return fail("response_format")
		}
		f, err = rawObject(f["json_schema"])
		if err != nil || only(f, "name", "description", "schema", "strict") != nil {
			return fail("response_format.json_schema")
		}
	} else if source == ResponsesProtocol {
		if only(f, "type", "name", "description", "schema", "strict") != nil {
			return fail("text.format")
		}
	} else if only(f, "type", "schema") != nil {
		return fail("output_config.format")
	}

	name := "response"
	strict, explicit := true, true
	if source != AnthropicProtocol {
		if bytes.Equal(bytes.TrimSpace(f["name"]), []byte("null")) || json.Unmarshal(f["name"], &name) != nil || !legalToolName.MatchString(name) {
			return fail("format.name")
		}
		v, ok := f["strict"]
		explicit, strict = ok, false
		if ok && (!bytes.Equal(bytes.TrimSpace(v), []byte("true")) && !bytes.Equal(bytes.TrimSpace(v), []byte("false"))) {
			return fail("format.strict")
		}
		if ok {
			_ = json.Unmarshal(v, &strict)
		}
	}
	if v, ok := f["description"]; ok {
		var description string
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &description) != nil {
			return fail("format.description")
		}
	}
	if target == AnthropicProtocol && (!explicit || !strict) {
		return fail("format.strict")
	}
	var schema object
	if decodeNumbers(f["schema"], &schema) != nil || schema == nil || schema["type"] != "object" || !outputSchemaNode(schema, strict, 0) {
		return fail("format.schema")
	}

	// Retain the original JSON schema bytes; even optional fields remain optional.
	out := object{"schema": f["schema"]}
	if target == AnthropicProtocol {
		out["type"] = "json_schema"
	} else {
		out["name"] = name
		if explicit {
			out["strict"] = strict
		}
		if v, ok := f["description"]; ok {
			out["description"] = v
		}
		if target == ChatProtocol {
			out = object{"type": "json_schema", "json_schema": out}
		} else {
			out["type"] = "json_schema"
		}
	}
	return json.Marshal(out)
}

// uniqueFormatKeys rejects duplicate keys instead of silently choosing one of
// conflicting format declarations. It also visits schema and instance positions.
func uniqueFormatKeys(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return false
			}
			keys[name] = true
		}
		if !uniqueFormatKeys(d) {
			return false
		}
	}
	_, err = d.Token()
	return err == nil
}

// Only these schema keywords have a directly supported meaning in every target.
// Visit schema positions explicitly; property names and enum data are not keywords.
func outputSchemaNode(node any, strict bool, depth int) bool {
	x, ok := node.(map[string]any)
	if !ok || depth > 64 {
		return false
	}
	for key, value := range x {
		switch key {
		case "type", "properties", "required", "additionalProperties", "items", "anyOf":
		case "title", "description":
			if _, ok := value.(string); !ok {
				return false
			}
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 {
				return false
			}
			for i, item := range values {
				switch item.(type) {
				case nil, string, bool, json.Number:
				default:
					return false
				}
				for _, previous := range values[:i] {
					if item == previous {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	types := map[string]bool{}
	switch value := x["type"].(type) {
	case string:
		types[value] = true
	case []any:
		if len(value) == 0 {
			return false
		}
		for _, v := range value {
			s, ok := v.(string)
			if !ok || types[s] {
				return false
			}
			types[s] = true
		}
	case nil:
		if _, exists := x["type"]; exists {
			return false
		}
		if _, ok := x["anyOf"]; !ok {
			return false
		}
	default:
		return false
	}
	for typ := range types {
		switch typ {
		case "object", "array", "string", "integer", "number", "boolean", "null":
		default:
			return false
		}
	}
	if value, ok := x["anyOf"]; ok {
		children, ok := value.([]any)
		if !ok || len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !outputSchemaNode(child, strict, depth+1) {
				return false
			}
		}
	}
	props := map[string]any{}
	if value, ok := x["properties"]; ok {
		var yes bool
		props, yes = value.(map[string]any)
		if !yes || !types["object"] {
			return false
		}
		for _, child := range props {
			if !outputSchemaNode(child, strict, depth+1) {
				return false
			}
		}
	}
	required := map[string]bool{}
	if value, ok := x["required"]; ok {
		values, ok := value.([]any)
		if !ok || !types["object"] {
			return false
		}
		for _, item := range values {
			s, ok := item.(string)
			if !ok || required[s] {
				return false
			}
			if _, ok := props[s]; !ok {
				return false
			}
			required[s] = true
		}
	}
	if value, ok := x["additionalProperties"]; ok {
		if _, ok := value.(bool); !ok || !types["object"] {
			return false
		}
	}
	if strict && types["object"] && (x["additionalProperties"] != false || len(props) != len(required)) {
		return false
	}
	if types["array"] {
		if !outputSchemaNode(x["items"], strict, depth+1) {
			return false
		}
	} else if _, ok := x["items"]; ok {
		return false
	}
	return true
}
