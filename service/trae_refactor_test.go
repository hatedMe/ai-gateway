package service

import (
	"testing"
	"traecli-api-proxy/model"

	"github.com/stretchr/testify/assert"
)

func TestConvertClaudeMessagesToTrae_Refactor_Base(t *testing.T) {
	// 基础测试用例：简单文本消息
	msg := []model.ClaudeMessage{
		{Role: "user", Content: "Hello"},
	}
	traeMsgs, userInput, err := ConvertClaudeMessagesToTrae(msg, nil)

	assert.NoError(t, err)
	assert.Equal(t, "Hello", userInput)
	assert.Len(t, traeMsgs, 1)
	assert.Equal(t, "user", traeMsgs[0].Role)
	assert.Equal(t, "Hello", traeMsgs[0].Content[0].Text)
}

func TestConvertClaudeMessagesToTrae_Refactor_ToolUse(t *testing.T) {
	// 工具调用用例
	msg := []model.ClaudeMessage{
		{
			Role: "user",
			Content: []any{
				map[string]any{
					"type": "tool_use",
					"id":   "tool_123",
					"name": "calc",
					"input": map[string]any{"x": 1},
				},
			},
		},
	}
	traeMsgs, _, err := ConvertClaudeMessagesToTrae(msg, nil)

	assert.NoError(t, err)
	assert.Len(t, traeMsgs[0].ToolCalls, 1)
	assert.Equal(t, "tool_123", traeMsgs[0].ToolCalls[0]["id"])
}
