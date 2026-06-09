package model

type TraeRequest struct {
	ConfigName     string        `json:"config_name"`
	ConversationID string        `json:"conversation_id"`
	Messages       []TraeMessage `json:"messages"`
	ModelName      string        `json:"model_name"`
	SessionID      string        `json:"session_id"`
	Tools          []any         `json:"tools"`
	UserInput      string        `json:"user_input"`
}

type TraeMessage struct {
	Role       string           `json:"role"`
	Content    []TraeContent    `json:"content"`
	ToolCalls  []map[string]any `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type TraeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TraeToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type TraeToolCall struct {
	Index        int                  `json:"index"`
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	FunctionCall TraeToolCallFunction `json:"function_call"`
}

type TraeOutput struct {
	Response         string         `json:"response"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []TraeToolCall `json:"tool_calls"`
}

type TraeTokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type TraeDone struct {
	FinishReason string `json:"finish_reason"`
}

type TraeError struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
}

type AccumulatedToolCall struct {
	ID        string
	Name      string
	Arguments string
}
