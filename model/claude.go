package model

type ClaudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []ClaudeMessage `json:"messages"`
	System    interface{}     `json:"system,omitempty"`
	Stream    bool            `json:"stream"`
	Tools     []interface{}   `json:"tools,omitempty"`
}

type ClaudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ClaudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   interface{}          `json:"stop_reason"`
	StopSequence interface{}          `json:"stop_sequence"`
	Usage        ClaudeUsage          `json:"usage"`
}

type ClaudeContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
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
	Type         string      `json:"type"`
	Index        int         `json:"index"`
	ContentBlock interface{} `json:"content_block"`
}

type ClaudeStreamContentBlockDelta struct {
	Type  string      `json:"type"`
	Index int         `json:"index"`
	Delta interface{} `json:"delta"`
}

type ClaudeThinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type ClaudeTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
	StopReason   string      `json:"stop_reason"`
	StopSequence interface{} `json:"stop_sequence"`
}
