package service

import (
	"testing"
	"traecli-api-proxy/model"

	"github.com/stretchr/testify/assert"
)

func TestConvertOpenAIMessagesToTrae(t *testing.T) {
	messages := []model.OpenAIMessage{
		{
			Role:      "assistant",
			ToolCalls: []any{map[string]any{"id": "call_1", "function": map[string]any{"name": "test", "arguments": "{}"}}},
		},
		{
			Role:    "user",
			Content: "Hello",
		},
	}

	result, userInput := ConvertOpenAIMessagesToTrae(messages)

	assert.Len(t, result, 2)
	assert.Equal(t, "Hello", userInput)
	assert.Equal(t, "assistant", result[0].Role)
	assert.Equal(t, "user", result[1].Role)
	assert.Len(t, result[0].ToolCalls, 1)
}
