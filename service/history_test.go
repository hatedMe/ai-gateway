package service

import (
	"testing"
	"traecli-api-proxy/model"
)

func TestConvertClaudeMessagesToTrae_ToolHistory(t *testing.T) {
	// 模拟一个包含工具调用历史的消息序列：
	// user 提问 → assistant 发起 tool_use → user 回传 tool_result
	messages := []model.ClaudeMessage{
		{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "What is the weather?"},
			},
		},
		{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "I will check."},
				map[string]interface{}{
					"type":  "tool_use",
					"id":    "call_123",
					"name":  "get_weather",
					"input": map[string]interface{}{"city": "Beijing"},
				},
			},
		},
		{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": "call_123",
					"content":     "Sunny, 25C",
				},
			},
		},
	}

	result, userInput := ConvertClaudeMessagesToTrae(messages, nil)

	// 1. 三条消息全部转换
	if len(result) != 3 {
		t.Fatalf("expected 3 trae messages, got %d", len(result))
	}

	// 2. assistant 消息携带 tool_calls
	if result[1].Role != "assistant" {
		t.Errorf("expected role assistant, got %s", result[1].Role)
	}
	if len(result[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result[1].ToolCalls))
	}
	if result[1].ToolCalls[0]["id"] != "call_123" {
		t.Errorf("expected id call_123, got %v", result[1].ToolCalls[0]["id"])
	}
	fn := result[1].ToolCalls[0]["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name get_weather, got %v", fn["name"])
	}
	// input 应被序列化为 JSON 字符串
	if fn["arguments"] != `{"city":"Beijing"}` {
		t.Errorf("expected arguments {\"city\":\"Beijing\"}, got %v", fn["arguments"])
	}

	// 3. tool_result 消息角色变为 tool，并设置 tool_call_id
	if result[2].Role != "tool" {
		t.Errorf("expected role tool, got %s", result[2].Role)
	}
	if result[2].ToolCallID != "call_123" {
		t.Errorf("expected tool_call_id call_123, got %s", result[2].ToolCallID)
	}

	// 4. 最后一条 user 消息的 content 提取为 userInput
	if userInput != "Sunny, 25C" {
		t.Errorf("expected userInput 'Sunny, 25C', got %q", userInput)
	}
}

func TestConvertClaudeMessagesToTrae_ToolResultIsError(t *testing.T) {
	// is_error=true 时，tool_result 内容应加 "ERROR: " 前缀
	messages := []model.ClaudeMessage{
		{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": "call_456",
					"content":     "boom",
					"is_error":    true,
				},
			},
		},
	}

	result, _ := ConvertClaudeMessagesToTrae(messages, nil)

	if result[0].Role != "tool" {
		t.Fatalf("expected role tool, got %s", result[0].Role)
	}
	text := result[0].Content[0].Text
	if text != "ERROR: boom" {
		t.Errorf("expected 'ERROR: boom', got %q", text)
	}
}
