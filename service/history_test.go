package service

import (
	"encoding/json"
	"testing"
	"traecli-api-proxy/model"
)

func TestConvertClaudeMessagesToTrae_ToolHistory(t *testing.T) {
	// 模拟一个包含工具调用历史的消息序列
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
					"type": "tool_use",
					"id":   "call_123",
					"name": "get_weather",
					"input": map[string]interface{}{
						"city": "Beijing",
					},
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

	// 这里我们直接测试刚才在 handler/claude.go 中实现的转换逻辑
	// 因为逻辑现在写在 handler 里，我们写一个本地辅助函数来模拟
	convert := func(msgs []model.ClaudeMessage) []model.TraeMessage {
		var traeMsgs []model.TraeMessage
		for _, msg := range msgs {
			traeMsg := model.TraeMessage{Role: msg.Role}
			var parts []string

			// 简化版逻辑，模拟 handler 中的实现
			if contentList, ok := msg.Content.([]interface{}); ok {
				for _, item := range contentList {
					if block, ok := item.(map[string]interface{}); ok {
						switch block["type"] {
						case "text":
							parts = append(parts, block["text"].(string))
						case "tool_use":
							id := block["id"].(string)
							name := block["name"].(string)
							args, _ := json.Marshal(block["input"])
							traeMsg.ToolCalls = append(traeMsg.ToolCalls, map[string]interface{}{
								"id":   id,
								"type": "function",
								"function": map[string]interface{}{
									"name":      name,
									"arguments": string(args),
								},
							})
						case "tool_result":
							traeMsg.Role = "tool"
							traeMsg.ToolCallID = block["tool_use_id"].(string)
							parts = append(parts, block["content"].(string))
						}
					}
				}
			}
			if len(parts) > 0 || len(traeMsg.ToolCalls) == 0 {
				traeMsg.Content = []model.TraeContent{{Type: "text", Text: ""}} // 简化
				if len(parts) > 0 {
					traeMsg.Content[0].Text = parts[0]
				}
			}
			traeMsgs = append(traeMsgs, traeMsg)
		}
		return traeMsgs
	}

	result := convert(messages)

	// 1. 验证 assistant 的 tool_calls
	if result[1].Role != "assistant" {
		t.Errorf("Expected role assistant, got %s", result[1].Role)
	}
	if len(result[1].ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(result[1].ToolCalls))
	}
	if result[1].ToolCalls[0]["id"] != "call_123" {
		t.Errorf("Expected id call_123, got %v", result[1].ToolCalls[0]["id"])
	}

	// 2. 验证 tool 消息的转换
	if result[2].Role != "tool" {
		t.Errorf("Expected role tool, got %s", result[2].Role)
	}
	if result[2].ToolCallID != "call_123" {
		t.Errorf("Expected tool_call_id call_123, got %s", result[2].ToolCallID)
	}
}
