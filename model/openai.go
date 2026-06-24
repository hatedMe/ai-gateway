package model

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []any           `json:"tools"`
}

type OpenAIMessage struct {
	Role             string `json:"role"`
	Content          any    `json:"content,omitempty"`
	ReasoningContent any    `json:"reasoning_content,omitempty"`
	ToolCalls        any    `json:"tool_calls,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
}

type OpenAIToolCall struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function OpenAIToolCallFunction `json:"function"`
}

type OpenAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object,omitempty"`
	Created int64               `json:"created,omitempty"`
	Model   string              `json:"model,omitempty"`
	Choices []OpenAIChunkChoice `json:"choices"`
}

type OpenAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason any         `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role             string `json:"role,omitempty"`
	Content          any    `json:"content,omitempty"`
	ReasoningContent any    `json:"reasoning_content,omitempty"`
	ToolCalls        any    `json:"tool_calls,omitempty"`
}
