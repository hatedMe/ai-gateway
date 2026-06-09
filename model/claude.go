package model

type ClaudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []ClaudeMessage `json:"messages"`
	System    any             `json:"system,omitempty"`
	Stream    bool            `json:"stream"`
	Tools     []any           `json:"tools,omitempty"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ClaudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   any                  `json:"stop_reason"`
	StopSequence any                  `json:"stop_sequence"`
	Usage        ClaudeUsage          `json:"usage"`
}

type ClaudeContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
}

type ClaudeToolUseContentBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ClaudeThinkingContentBlock struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type ClaudeTextContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ClaudeStreamMessageStart struct {
	Type    string         `json:"type"`
	Message ClaudeResponse `json:"message"`
}

type ClaudeStreamContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock any    `json:"content_block"`
}

type ClaudeStreamContentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta any    `json:"delta"`
}

type ClaudeThinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type ClaudeTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ClaudeInputJsonDelta struct {
	Type        string `json:"type"`
	PartialJson string `json:"partial_json"`
}

type ClaudeStreamContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type ClaudeStreamMessageDelta struct {
	Type  string             `json:"type"`
	Delta ClaudeMessageDelta `json:"delta"`
	Usage ClaudeUsage        `json:"usage"`
}

type ClaudeMessageDelta struct {
	StopReason   string `json:"stop_reason"`
	StopSequence any    `json:"stop_sequence"`
}
