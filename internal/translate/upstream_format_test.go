package translate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const outputSchemaFixture = `{"type":"object","properties":{"status":{"type":"string","enum":["ok","error"]},"reason":{"type":["string","null"]}},"required":["status","reason"],"additionalProperties":false}`

func outputFormatFixture(protocol Protocol, schema string) []byte {
	switch protocol {
	case ChatProtocol:
		return []byte(`{"type":"json_schema","json_schema":{"name":"result","schema":` + schema + `,"strict":true}}`)
	case ResponsesProtocol:
		return []byte(`{"type":"json_schema","name":"result","schema":` + schema + `,"strict":true}`)
	default:
		return []byte(`{"type":"json_schema","schema":` + schema + `}`)
	}
}

func TestCrossProtocolJSONSchema(t *testing.T) {
	protocols := []Protocol{ChatProtocol, ResponsesProtocol, AnthropicProtocol}
	for _, source := range protocols {
		for _, target := range protocols {
			t.Run(string(source)+"/"+string(target), func(t *testing.T) {
				raw := outputFormatFixture(source, outputSchemaFixture)
				before := string(raw)
				got, err := ConvertOutputFormat(raw, source, target, true)
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != before {
					t.Fatal("source format mutated")
				}
				var out map[string]json.RawMessage
				if err := json.Unmarshal(got, &out); err != nil {
					t.Fatal(err)
				}
				if target == ChatProtocol {
					if err := json.Unmarshal(out["json_schema"], &out); err != nil {
						t.Fatal(err)
					}
				}
				var schema, want any
				_ = json.Unmarshal(out["schema"], &schema)
				_ = json.Unmarshal([]byte(outputSchemaFixture), &want)
				if !reflect.DeepEqual(schema, want) {
					t.Fatalf("schema altered: %s", got)
				}
				if target != AnthropicProtocol {
					name := `"result"`
					if source == AnthropicProtocol {
						name = `"response"`
					}
					if string(out["name"]) != name || string(out["strict"]) != "true" {
						t.Fatalf("metadata: %s", got)
					}
				} else if len(out) != 2 {
					t.Fatalf("non-native Anthropic fields: %s", got)
				}
			})
		}
	}
	// OpenAI non-strict intent is preserved between protocols which represent it.
	raw := []byte(`{"type":"json_schema","name":"result","strict":false,"description":"result description","schema":` + outputSchemaFixture + `}`)
	got, err := ConvertOutputFormat(raw, ResponsesProtocol, ChatProtocol, true)
	if err != nil || !strings.Contains(string(got), `"strict":false`) || !strings.Contains(string(got), `"description":"result description"`) {
		t.Fatalf("non-strict: %s, %v", got, err)
	}
	// Do not inject required fields, close objects, or change optionality.
	optional := `{"type":"object","properties":{"reason":{"type":"string"}}}`
	raw = []byte(`{"type":"json_schema","name":"result","schema":` + optional + `}`)
	got, err = ConvertOutputFormat(raw, ResponsesProtocol, ChatProtocol, true)
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Format struct {
			Schema json.RawMessage `json:"schema"`
			Strict json.RawMessage `json:"strict"`
		} `json:"json_schema"`
	}
	if err = json.Unmarshal(got, &chat); err != nil {
		t.Fatal(err)
	}
	if string(chat.Format.Schema) != optional || chat.Format.Strict != nil {
		t.Fatalf("optional intent changed: %s", got)
	}
}

func TestUnsupportedStructuredOutput(t *testing.T) {
	cases := []struct {
		name           string
		raw            []byte
		source, target Protocol
		supported      bool
	}{
		{"undeclared", outputFormatFixture(ChatProtocol, outputSchemaFixture), ChatProtocol, ResponsesProtocol, false},
		{"non-strict Anthropic", []byte(`{"type":"json_schema","name":"result","strict":false,"schema":` + outputSchemaFixture + `}`), ResponsesProtocol, AnthropicProtocol, true},
		{"implicit strict Anthropic", []byte(`{"type":"json_schema","name":"result","schema":` + outputSchemaFixture + `}`), ResponsesProtocol, AnthropicProtocol, true},
		{"null strict", []byte(`{"type":"json_schema","name":"result","strict":null,"schema":` + outputSchemaFixture + `}`), ResponsesProtocol, ChatProtocol, true},
		{"conflict", []byte(`{"type":"json_schema","name":"secret-payload","schema":{},"json_schema":{}}`), ResponsesProtocol, ChatProtocol, true},
		{"invalid name", []byte(`{"type":"json_schema","name":"secret-payload!","schema":{}}`), ResponsesProtocol, ChatProtocol, true},
		{"null name", []byte(`{"type":"json_schema","name":null,"schema":` + outputSchemaFixture + `}`), ResponsesProtocol, ChatProtocol, true},
		{"duplicate strict", []byte(`{"type":"json_schema","name":"result","strict":true,"strict":false,"schema":` + outputSchemaFixture + `}`), ResponsesProtocol, ChatProtocol, true},
		{"duplicate schema property", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{"x":{"type":"string","type":"integer"}},"required":["x"],"additionalProperties":false}`), ChatProtocol, ResponsesProtocol, true},
		{"unknown business constraint", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{},"required":[],"additionalProperties":false,"oneOf":[]}`), ChatProtocol, ResponsesProtocol, true},
		{"malformed nested type", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{"x":{"type":null,"anyOf":[{"type":"string"}]}},"required":["x"],"additionalProperties":false}`), ChatProtocol, ResponsesProtocol, true},
		{"Anthropic optional property", outputFormatFixture(AnthropicProtocol, `{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}`), AnthropicProtocol, ResponsesProtocol, true},
		{"unsupported mode", []byte(`{"type":"json_object"}`), ResponsesProtocol, AnthropicProtocol, true},
		{"optional property strict", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}`), ChatProtocol, ResponsesProtocol, true},
		{"open object strict", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{}}`), ChatProtocol, ResponsesProtocol, true},
		{"unsupported constraint", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{"x":{"type":"integer","minimum":5}},"required":["x"],"additionalProperties":false}`), ChatProtocol, AnthropicProtocol, true},
		{"invalid enum", outputFormatFixture(ChatProtocol, `{"type":"object","properties":{"x":{"type":"string","enum":"secret-payload"}},"required":["x"],"additionalProperties":false}`), ChatProtocol, ResponsesProtocol, true},
		{"unknown protocol", outputFormatFixture(ChatProtocol, outputSchemaFixture), ChatProtocol, Protocol("unknown"), true},
		{"null", []byte(`null`), ChatProtocol, ResponsesProtocol, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ConvertOutputFormat(tc.raw, tc.source, tc.target, tc.supported)
			var diagnostic *ConversionError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("expected ConversionError, got %v", err)
			}
			if diagnostic.Code == "" || diagnostic.Stage == "" || diagnostic.Field == "" {
				t.Fatalf("missing diagnostic: %#v", diagnostic)
			}
			if strings.Contains(err.Error(), "secret-payload") {
				t.Fatalf("payload leaked: %v", err)
			}
		})
	}
}
