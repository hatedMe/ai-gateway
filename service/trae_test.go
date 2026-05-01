package service

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"traecli-api-proxy/config"
	"traecli-api-proxy/model"
)

func setupTestConfig(t *testing.T) {
	t.Helper()
	content := `
server:
  port: 3000
trae:
  headers:
    X-App-Id: "test-app-id"
`
	tmpFile, err := os.CreateTemp("", "service-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })
	tmpFile.WriteString(content)
	tmpFile.Close()
	if err := config.Load(tmpFile.Name()); err != nil {
		t.Fatalf("Load config failed: %v", err)
	}
}

func TestContentToString_String(t *testing.T) {
	result := ContentToString("hello")
	if result != "hello" {
		t.Errorf("expected hello, got %s", result)
	}
}

func TestContentToString_Nil(t *testing.T) {
	result := ContentToString(nil)
	if result != "null" {
		t.Errorf("expected null, got %s", result)
	}
}

func TestContentToString_Map(t *testing.T) {
	result := ContentToString(map[string]string{"key": "value"})
	expected := `{"key":"value"}`
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestClaudeContentToString_String(t *testing.T) {
	result := ClaudeContentToString("hello world")
	if result != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result)
	}
}

func TestClaudeContentToString_ContentBlocks(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "text", "text": "hello"},
		map[string]interface{}{"type": "text", "text": "world"},
	}
	result := ClaudeContentToString(input)
	if result != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got '%s'", result)
	}
}

func TestClaudeContentToString_NonTextBlock(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "image", "source": "data"},
		map[string]interface{}{"type": "text", "text": "hello"},
	}
	result := ClaudeContentToString(input)
	if result != "hello" {
		t.Errorf("expected 'hello', got '%s'", result)
	}
}

func TestConvertClaudeToolsToTrae_AlreadyOpenAIFormat(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "get_weather",
				"description": "Get weather",
				"parameters":  `{"type":"object"}`,
			},
		},
	}
	result := ConvertClaudeToolsToTrae(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	tool := result[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("expected type function, got %v", tool["type"])
	}
}

func TestConvertClaudeToolsToTrae_ClaudeFormat(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{
			"name":        "get_weather",
			"description": "Get weather info",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	result := ConvertClaudeToolsToTrae(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	tool := result[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("expected type function, got %v", tool["type"])
	}
	fn := tool["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name get_weather, got %v", fn["name"])
	}
	paramsStr, ok := fn["parameters"].(string)
	if !ok {
		t.Errorf("expected parameters to be string, got %T", fn["parameters"])
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(paramsStr), &params); err != nil {
		t.Errorf("parameters is not valid JSON: %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("expected schema type object, got %v", params["type"])
	}
}

func TestConvertClaudeToolsToTrae_EmptyName(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{
			"description": "no name tool",
		},
	}
	result := ConvertClaudeToolsToTrae(input)
	if len(result) != 0 {
		t.Errorf("expected 0 tools for empty name, got %d", len(result))
	}
}

func TestConvertClaudeToolsToTrae_Nil(t *testing.T) {
	result := ConvertClaudeToolsToTrae(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 tools for nil, got %d", len(result))
	}
}

func TestReadTraeSSE(t *testing.T) {
	sseData := "event: output\ndata: {\"response\":\"hello\"}\n\nevent: done\ndata: {\"finish_reason\":\"stop\"}\n\n"
	body := io.NopCloser(strings.NewReader(sseData))

	var events []string
	var datas []string
	ReadTraeSSE(body, func(event string, data string) bool {
		events = append(events, event)
		datas = append(datas, data)
		return true
	})

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0] != "output" {
		t.Errorf("expected first event output, got %s", events[0])
	}
	if events[1] != "done" {
		t.Errorf("expected second event done, got %s", events[1])
	}

	var out model.TraeOutput
	json.Unmarshal([]byte(datas[0]), &out)
	if out.Response != "hello" {
		t.Errorf("expected response hello, got %s", out.Response)
	}
}

func TestReadTraeSSE_StopEarly(t *testing.T) {
	sseData := "event: output\ndata: {\"response\":\"hello\"}\n\nevent: output\ndata: {\"response\":\"world\"}\n\nevent: done\ndata: {\"finish_reason\":\"stop\"}\n\n"
	body := io.NopCloser(strings.NewReader(sseData))

	count := 0
	ReadTraeSSE(body, func(event string, data string) bool {
		count++
		return false
	})

	if count != 1 {
		t.Errorf("expected 1 event after early stop, got %d", count)
	}
}

func TestReadTraeSSE_EmptyData(t *testing.T) {
	sseData := "event: output\ndata: \n\nevent: done\ndata: {\"finish_reason\":\"stop\"}\n\n"
	body := io.NopCloser(strings.NewReader(sseData))

	count := 0
	ReadTraeSSE(body, func(event string, data string) bool {
		count++
		return true
	})

	if count != 1 {
		t.Errorf("expected 1 event (empty data skipped), got %d", count)
	}
}

func TestBuildTraeRequest(t *testing.T) {
	setupTestConfig(t)
	messages := []model.TraeMessage{
		{Role: "user", Content: []model.TraeContent{{Type: "text", Text: "hello"}}},
	}
	req := BuildTraeRequest(messages, "glm-5.1", nil, "hello")
	if req.ConfigName != "glm-5.1" {
		t.Errorf("expected ConfigName glm-5.1, got %s", req.ConfigName)
	}
	if req.ModelName != "glm-5__v2" {
		t.Errorf("expected ModelName glm-5__v2, got %s", req.ModelName)
	}
	if req.UserInput != "hello" {
		t.Errorf("expected UserInput hello, got %s", req.UserInput)
	}
	if req.ConversationID == "" {
		t.Error("expected non-empty ConversationID")
	}
	if req.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}
}
