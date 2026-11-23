package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareRequestCompatibility(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":64000,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"YQ=="}}]}],"tools":[{"type":"custom","name":"mcp/tool","input_schema":{"type":"object","properties":{}},"strict":true,"input_examples":[{}],"defer_loading":true}],"stop_sequences":["END"],"top_k":4,"output_config":{"effort":"max","format":{"type":"json_schema","schema":{"type":"object","properties":{},"additionalProperties":false}}}}`)
	p, err := PrepareRequest(raw, RequestOptions{ReasoningEfforts: []string{"low", "high"}})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(p.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(p.StopSequences) != 1 || len(p.Warnings) < 3 || len(p.ToolNames) != 1 {
		t.Fatalf("missing context: %+v", p)
	}
	if out["reasoning"].(map[string]any)["effort"] != "high" || out["text"] == nil || !strings.Contains(string(p.Body), "input_file") {
		t.Fatal(string(p.Body))
	}
}

func TestPrepareRequestRejectsInvalidCompatibility(t *testing.T) {
	for _, extra := range []string{`"top_k":0`, `"system":[{"type":"text","text":"s","cache_control":{"type":"permanent"}}]`, `"thinking":{"type":"disabled"}`} {
		_, err := PrepareRequest([]byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"x"}],`+extra+`}`), RequestOptions{ReasoningEfforts: []string{"high"}})
		if err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
}
func TestPrepareRequestToolsHistoryAndDowngrades(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":4096,"tools":[{"name":"mcp/server.tool","type":"custom","strict":true,"input_schema":{"type":"object","properties":{"q":{"type":"string"}}}},{"type":"web_search_20250305","name":"web_search"},{"type":"code_execution_20250825","name":"code_execution"}],"tool_choice":{"type":"tool","name":"mcp/server.tool"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"anthropic"},{"type":"redacted_thinking","data":"opaque"},{"type":"tool_use","id":"c","name":"mcp/server.tool","input":{"q":"x"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]}]}]}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := decode(t, p.Body)
	tools := out["tools"].([]any)
	name := tools[0].(map[string]any)["name"].(string)
	if len(tools) != 3 || p.ToolNames[name] != "mcp/server.tool" || out["tool_choice"].(map[string]any)["name"] != name {
		t.Fatal(string(p.Body))
	}
	input := out["input"].([]any)
	if input[0].(map[string]any)["name"] != name || !strings.Contains(string(p.Body), "file_url") || strings.Contains(string(p.Body), "encrypted_content") {
		t.Fatal(string(p.Body))
	}
	schema := tools[0].(map[string]any)["parameters"].(map[string]any)
	if schema["additionalProperties"] != false || len(schema["required"].([]any)) != 1 {
		t.Fatal(schema)
	}
}

func TestPrepareRequestBuiltinChoiceAndDocumentContext(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":100,"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}],"tool_choice":{"type":"auto"},"messages":[{"role":"user","content":[{"type":"document","title":"Report","context":"Annual report context","source":{"type":"url","url":"https://example.com/report.pdf"}}]}]}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Body), "Annual report context") {
		t.Fatal(string(p.Body))
	}
}
func TestPrepareRequestTextDocumentsAndServerHistory(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"s","name":"web_search","input":{"query":"news"}}]},{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"s","content":[{"title":"news","url":"https://example.com"}]},{"type":"document","source":{"type":"content","content":[{"type":"text","text":"Document body"}]}},{"type":"search_result","title":"Result","source":"https://example.com","content":[{"type":"text","text":"Search snippet"}]}]}]}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) == 0 || !strings.Contains(string(p.Body), "Document body") || !strings.Contains(string(p.Body), "Search snippet") || strings.Contains(string(p.Body), "function_call") {
		t.Fatal(string(p.Body))
	}
}

func TestStrictSchemaTraversesSchemaPositions(t *testing.T) {
	raw := []byte(`{"type":"object","properties":{"nullable":{"type":["object","null"],"properties":{"x":{"type":"string"}}},"default":{"type":"object","properties":{"y":{"type":"string"}}},"enum":{"type":"object","properties":{"z":{"type":"string"}}},"const":{"type":"object","properties":{"w":{"type":"string"}}},"type":{"type":"string"},"object":{"type":"string"},"literal":{"type":"object","properties":{},"const":{"type":"object","properties":{"keep":{"type":"string"}}}}},"required":["nullable","default","enum","const","type","object","literal"]}`)
	schema, err := rawObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateStrictSchema(schema); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(schema)
	out := decode(t, b)
	props := out["properties"].(map[string]any)
	for _, name := range []string{"nullable", "default", "enum", "const"} {
		node := props[name].(map[string]any)
		if node["additionalProperties"] != false || len(node["required"].([]any)) != 1 {
			t.Fatalf("%s: %+v", name, node)
		}
	}
	literal := props["literal"].(map[string]any)["const"].(map[string]any)
	if _, ok := literal["additionalProperties"]; ok {
		t.Fatalf("modified literal: %+v", literal)
	}
	if props["type"].(map[string]any)["type"] != "string" || props["object"].(map[string]any)["type"] != "string" {
		t.Fatal(props)
	}
}

func TestPrepareCodeExecutionOptionWarning(t *testing.T) {
	p, err := PrepareRequest([]byte(`{"model":"m","max_tokens":100,"tools":[{"type":"code_execution_20250825","name":"code_execution","max_uses":3}],"messages":[{"role":"user","content":"calculate"}]}`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) == 0 || !strings.Contains(strings.Join(p.Warnings, " "), "max_uses") {
		t.Fatalf("silent dropped field: %+v", p)
	}
}

func TestPrepareNestedDocumentContextAndCitations(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":{"citations":"user data"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"document","context":"ONLY_ONCE","citations":{"enabled":true},"source":{"type":"text","media_type":"text/plain","data":"body"}},{"type":"search_result","title":"title","source":"url","content":[{"type":"text","text":"snippet"}],"citations":{"enabled":true}}]}]}]}`)
	p, err := PrepareRequest(raw, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(p.Body), "ONLY_ONCE") != 1 || !strings.Contains(strings.Join(p.Warnings, " "), "citation") {
		t.Fatalf("body=%s warnings=%v", p.Body, p.Warnings)
	}
}
