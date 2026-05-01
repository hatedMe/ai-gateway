package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []interface{}   `json:"tools"`
}

type OpenAIMessage struct {
	Role             string      `json:"role"`
	Content          interface{} `json:"content,omitempty"`
	ReasoningContent interface{} `json:"reasoning_content,omitempty"`
	ToolCalls        interface{} `json:"tool_calls,omitempty"`
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
	FinishReason interface{} `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role             string      `json:"role,omitempty"`
	Content          interface{} `json:"content,omitempty"`
	ReasoningContent interface{} `json:"reasoning_content,omitempty"`
	ToolCalls        interface{} `json:"tool_calls,omitempty"`
}

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

type TraeRequest struct {
	ConfigName     string        `json:"config_name"`
	ConversationID string        `json:"conversation_id"`
	Messages       []TraeMessage `json:"messages"`
	ModelName      string        `json:"model_name"`
	SessionID      string        `json:"session_id"`
	Tools          []interface{} `json:"tools"`
	UserInput      string        `json:"user_input"`
}

type TraeMessage struct {
	Role    string        `json:"role"`
	Content []TraeContent `json:"content"`
}

type TraeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TraeOutput struct {
	Response         string      `json:"response"`
	ReasoningContent string      `json:"reasoning_content"`
	ToolCalls        interface{} `json:"tool_calls"`
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

type ModelConfig struct {
	ConfigName string
	ModelName  string
}

var modelMap = map[string]ModelConfig{
	"Doubao-Seed-2.0-Code": {ConfigName: "Doubao-Seed-2.0-Code", ModelName: "Doubao-Seed-2.0-Code__v2"},
	"Doubao_1_6":           {ConfigName: "Doubao_1_6", ModelName: "Doubao_1_6__v2"},
	"glm-5.1":              {ConfigName: "glm-5.1", ModelName: "glm-5__v2"},
	"glm-5":                {ConfigName: "glm-5", ModelName: "glm-5__v2"},
	"glm-4.7":              {ConfigName: "glm-4.7", ModelName: "glm-4.7__v2"},
	"minimax-m2.7":         {ConfigName: "minimax-m2.7", ModelName: "minimax-m2.7__v2"},
	"minimax-m2.5":         {ConfigName: "minimax-m2.5", ModelName: "minimax-m2.5__v2"},
	"qwen3-coder":          {ConfigName: "qwen3-coder", ModelName: "qwen3-coder__v2"},
	"kimi-k2.6":            {ConfigName: "kimi-k2.6", ModelName: "kimi-k2.6__v2"},
	"kimi-k2.5":            {ConfigName: "kimi-k2.5", ModelName: "kimi-k2.5__v2"},
	"deepseek-V3.2":        {ConfigName: "deepseek-V3.2", ModelName: "deepseek-V3.2__v2"},
	"deepseek-V3.1":        {ConfigName: "deepseek-V3.1", ModelName: "deepseek-V3.1__v2"},
}

const DefaultModel = "glm-5.1"
const TargetURL = "https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat"

func getModelConfig(model string) ModelConfig {
	if cfg, ok := modelMap[model]; ok {
		return cfg
	}
	return ModelConfig{ConfigName: model, ModelName: model + "__v2"}
}

func handleModels(c *gin.Context) {
	type ModelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var models []ModelItem
	for name := range modelMap {
		models = append(models, ModelItem{
			ID:      name,
			Object:  "model",
			Created: 1750118400,
			OwnedBy: "trae",
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

func main() {
	_ = godotenv.Load()
	r := gin.Default()
	r.POST("/v1/chat/completions", handleChatCompletions)
	r.POST("/v1/messages", handleClaudeMessages)
	r.GET("/v1/models", handleModels)
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	fmt.Printf("Server running on port %s\n", port)
	r.Run(":" + port)
}

func buildTraeRequest(traeMessages []TraeMessage, model string, tools []interface{}, userInput string) TraeRequest {
	modelCfg := getModelConfig(model)
	return TraeRequest{
		ConfigName:     modelCfg.ConfigName,
		ConversationID: uuid.New().String(),
		Messages:       traeMessages,
		ModelName:      modelCfg.ModelName,
		SessionID:      uuid.New().String(),
		Tools:          tools,
		UserInput:      userInput,
	}
}

func sendTraeRequest(traePayload TraeRequest) (*resty.Response, error) {
	auth := os.Getenv("TRAE_AUTH")
	if auth == "" {
		return nil, fmt.Errorf("TRAE_AUTH not configured")
	}

	client := resty.New()
	return client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("User-Agent", "Go-http-client/1.1").
		SetHeader("Host", "console.enterprise.trae.cn").
		SetHeader("X-App-Id", "7b3f9dc2-8a4e-5c6d-2f1b-9e4a3c5b7df0").
		SetHeader("X-Ide-Function", "chat").
		SetHeader("X-Ide-Version-Code", "20260206").
		SetBody(traePayload).
		SetDoNotParseResponse(true).
		Post(TargetURL)
}

func handleChatCompletions(c *gin.Context) {
	var req OpenAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Model == "" {
		req.Model = DefaultModel
	}

	traeMessages := make([]TraeMessage, len(req.Messages))
	var userInput string
	for i, msg := range req.Messages {
		contentStr := contentToString(msg.Content)
		traeMessages[i] = TraeMessage{Role: msg.Role, Content: []TraeContent{{Type: "text", Text: contentStr}}}
		if i == len(req.Messages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}

	traePayload := buildTraeRequest(traeMessages, req.Model, req.Tools, userInput)

	payloadJSON, _ := json.Marshal(traePayload)
	fmt.Printf("[OpenAI] Sending to Trae: %s\n", string(payloadJSON))

	resp, err := sendTraeRequest(traePayload)
	if err != nil {
		fmt.Printf("[OpenAI] Request error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	fmt.Printf("[OpenAI] Response status: %s\n", resp.Status())

	if resp.StatusCode() != 200 {
		bodyBytes, _ := io.ReadAll(resp.RawBody())
		fmt.Printf("[OpenAI] Error response body: %s\n", string(bodyBytes))
		c.JSON(resp.StatusCode(), gin.H{"error": string(bodyBytes)})
		return
	}

	defer resp.RawBody().Close()
	if req.Stream {
		handleOpenAIStream(c, resp.RawBody(), req.Model)
	} else {
		handleOpenAINonStream(c, resp.RawBody(), req.Model)
	}
}

func handleClaudeMessages(c *gin.Context) {
	var req ClaudeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	if req.Model == "" {
		req.Model = "claude-3-5-sonnet-latest"
	}

	traeMessages := []TraeMessage{}
	if req.System != nil {
		systemStr := contentToString(req.System)
		traeMessages = append(traeMessages, TraeMessage{Role: "system", Content: []TraeContent{{Type: "text", Text: systemStr}}})
	}

	var userInput string
	for i, msg := range req.Messages {
		contentStr := claudeContentToString(msg.Content)
		traeMessages = append(traeMessages, TraeMessage{Role: msg.Role, Content: []TraeContent{{Type: "text", Text: contentStr}}})
		if i == len(req.Messages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}

	traePayload := buildTraeRequest(traeMessages, req.Model, convertClaudeToolsToTrae(req.Tools), userInput)

	payloadJSON, _ := json.Marshal(traePayload)
	fmt.Printf("[Claude] Sending to Trae: %s\n", string(payloadJSON))

	resp, err := sendTraeRequest(traePayload)
	if err != nil {
		fmt.Printf("[Claude] Request error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"type": "error", "error": map[string]string{"type": "api_error", "message": err.Error()}})
		return
	}
	fmt.Printf("[Claude] Response status: %s\n", resp.Status())

	if resp.StatusCode() != 200 {
		bodyBytes, _ := io.ReadAll(resp.RawBody())
		fmt.Printf("[Claude] Error response body: %s\n", string(bodyBytes))
		c.JSON(resp.StatusCode(), gin.H{"type": "error", "error": map[string]string{"type": "api_error", "message": string(bodyBytes)}})
		return
	}

	defer resp.RawBody().Close()
	if req.Stream {
		handleClaudeStream(c, resp.RawBody(), req.Model)
	} else {
		handleClaudeNonStream(c, resp.RawBody(), req.Model)
	}
}

func contentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

func claudeContentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if block, ok := item.(map[string]interface{}); ok {
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

func convertClaudeToolsToTrae(claudeTools []interface{}) []interface{} {
	var traeTools []interface{}
	for _, tool := range claudeTools {
		if m, ok := tool.(map[string]interface{}); ok {
			if m["type"] == "function" {
				if _, ok := m["function"].(map[string]interface{}); ok {
					traeTools = append(traeTools, m)
					continue
				}
			}
			name, _ := m["name"].(string)
			desc, _ := m["description"].(string)
			inputSchema := m["input_schema"]
			if name != "" {
				var paramsStr string
				if inputSchema != nil {
					pj, _ := json.Marshal(inputSchema)
					paramsStr = string(pj)
				}
				traeTool := map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        name,
						"description": desc,
						"parameters":  paramsStr,
					},
				}
				traeTools = append(traeTools, traeTool)
			}
		}
	}
	return traeTools
}

func readTraeSSE(body io.ReadCloser, onEvent func(event string, data string) bool) {
	reader := bufio.NewReader(body)
	var currentEvent string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "" {
				continue
			}
			if !onEvent(currentEvent, dataStr) {
				return
			}
		}
	}
}

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	reader := bufio.NewReader(body)
	var currentEvent string
	c.Stream(func(w io.Writer) bool {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Stream read error: %v\n", err)
			}
			w.Write([]byte("data: [DONE]\n\n"))
			return false
		}

		line = strings.TrimSpace(line)
		if line == "" {
			return true
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "" {
				return true
			}

			if currentEvent == "error" {
				var traeErr TraeError
				json.Unmarshal([]byte(dataStr), &traeErr)
				errChunk := OpenAIChunk{
					ID:      "chatcmpl-" + uuid.New().String(),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []OpenAIChunkChoice{{Index: 0, Delta: OpenAIDelta{Content: traeErr.Error}}},
				}
				j, _ := json.Marshal(errChunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
				w.Write([]byte("data: [DONE]\n\n"))
				return false
			}

			var chunk OpenAIChunk
			chunk.ID = "chatcmpl-" + uuid.New().String()
			chunk.Object = "chat.completion.chunk"
			chunk.Created = time.Now().Unix()
			chunk.Model = model

			if currentEvent == "output" {
				var out TraeOutput
				json.Unmarshal([]byte(dataStr), &out)
				delta := OpenAIDelta{}
				if out.Response != "" {
					delta.Content = out.Response
				}
				if out.ReasoningContent != "" {
					delta.ReasoningContent = out.ReasoningContent
				}
				if out.ToolCalls != nil {
					delta.ToolCalls = out.ToolCalls
				}
				chunk.Choices = []OpenAIChunkChoice{{Index: 0, Delta: delta}}
				j, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
			} else if currentEvent == "done" {
				var done TraeDone
				json.Unmarshal([]byte(dataStr), &done)
				chunk.Choices = []OpenAIChunkChoice{{Index: 0, Delta: OpenAIDelta{}, FinishReason: done.FinishReason}}
				j, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
			}
		}
		return true
	})
}

func handleOpenAINonStream(c *gin.Context, body io.ReadCloser, model string) {
	var currentEvent string
	var accumulatedResponse string
	var accumulatedReasoning string
	var accumulatedToolCalls interface{}
	var usage OpenAIUsage

	readTraeSSE(body, func(event string, data string) bool {
		currentEvent = event
		switch currentEvent {
		case "error":
			var traeErr TraeError
			json.Unmarshal([]byte(data), &traeErr)
			c.JSON(http.StatusOK, OpenAIResponse{
				ID:      "chatcmpl-" + uuid.New().String(),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []OpenAIChoice{{Index: 0, Message: OpenAIMessage{Role: "assistant", Content: traeErr.Error}, FinishReason: "stop"}},
			})
			return false
		case "output":
			var out TraeOutput
			json.Unmarshal([]byte(data), &out)
			accumulatedResponse += out.Response
			accumulatedReasoning += out.ReasoningContent
			if out.ToolCalls != nil {
				accumulatedToolCalls = out.ToolCalls
			}
		case "token_usage":
			var tu TraeTokenUsage
			json.Unmarshal([]byte(data), &tu)
			usage = OpenAIUsage{PromptTokens: tu.PromptTokens, CompletionTokens: tu.CompletionTokens, TotalTokens: tu.TotalTokens}
		}
		return true
	})

	message := OpenAIMessage{Role: "assistant", Content: accumulatedResponse}
	if accumulatedReasoning != "" {
		message.ReasoningContent = accumulatedReasoning
	}
	if accumulatedToolCalls != nil {
		message.ToolCalls = accumulatedToolCalls
	}

	c.JSON(http.StatusOK, OpenAIResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{{Index: 0, Message: message, FinishReason: "stop"}},
		Usage:   usage,
	})
}

func writeSSE(c *gin.Context, eventType string, data interface{}) {
	j, _ := json.Marshal(data)
	c.Writer.Write([]byte("event: " + eventType + "\ndata: " + string(j) + "\n\n"))
	c.Writer.Flush()
}

func handleClaudeStream(c *gin.Context, body io.ReadCloser, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	msgID := "msg_" + uuid.New().String()

	writeSSE(c, "message_start", ClaudeStreamMessageStart{
		Type: "message_start",
		Message: ClaudeResponse{
			ID:      msgID,
			Type:    "message",
			Role:    "assistant",
			Content: []ClaudeContentBlock{},
			Model:   model,
			Usage:   ClaudeUsage{InputTokens: 0, OutputTokens: 0},
		},
	})

	thinkingBlockStarted := false
	textBlockStarted := false
	thinkingIndex := 0
	textIndex := 1
	var outputTokens int

	reader := bufio.NewReader(body)
	var currentEvent string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "" {
				continue
			}

			switch currentEvent {
			case "error":
				var traeErr TraeError
				json.Unmarshal([]byte(dataStr), &traeErr)
				if !textBlockStarted {
					writeSSE(c, "content_block_start", ClaudeStreamContentBlockStart{
						Type:         "content_block_start",
						Index:        textIndex,
						ContentBlock: ClaudeTextContentBlock{Type: "text", Text: ""},
					})
					textBlockStarted = true
				}
				writeSSE(c, "content_block_delta", ClaudeStreamContentBlockDelta{
					Type:  "content_block_delta",
					Index: textIndex,
					Delta: ClaudeTextDelta{Type: "text_delta", Text: traeErr.Error},
				})
				writeSSE(c, "content_block_stop", ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: textIndex})
				writeSSE(c, "message_delta", ClaudeStreamMessageDelta{
					Type:  "message_delta",
					Delta: ClaudeMessageDelta{StopReason: "end_turn"},
					Usage: ClaudeUsage{OutputTokens: 0},
				})
				writeSSE(c, "message_stop", map[string]string{"type": "message_stop"})
				return

			case "output":
				var out TraeOutput
				json.Unmarshal([]byte(dataStr), &out)

				if out.ReasoningContent != "" {
					if !thinkingBlockStarted {
						writeSSE(c, "content_block_start", ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        thinkingIndex,
							ContentBlock: ClaudeThinkingContentBlock{Type: "thinking", Thinking: ""},
						})
						thinkingBlockStarted = true
					}
					writeSSE(c, "content_block_delta", ClaudeStreamContentBlockDelta{
						Type:  "content_block_delta",
						Index: thinkingIndex,
						Delta: ClaudeThinkingDelta{Type: "thinking_delta", Thinking: out.ReasoningContent},
					})
				}

				if out.Response != "" {
					if thinkingBlockStarted && !textBlockStarted {
						writeSSE(c, "content_block_stop", ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: thinkingIndex})
						writeSSE(c, "content_block_start", ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        textIndex,
							ContentBlock: ClaudeTextContentBlock{Type: "text", Text: ""},
						})
						textBlockStarted = true
					}
					if !thinkingBlockStarted && !textBlockStarted {
						textIndex = 0
						writeSSE(c, "content_block_start", ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        textIndex,
							ContentBlock: ClaudeTextContentBlock{Type: "text", Text: ""},
						})
						textBlockStarted = true
					}
					writeSSE(c, "content_block_delta", ClaudeStreamContentBlockDelta{
						Type:  "content_block_delta",
						Index: textIndex,
						Delta: ClaudeTextDelta{Type: "text_delta", Text: out.Response},
					})
				}

			case "token_usage":
				var tu TraeTokenUsage
				json.Unmarshal([]byte(dataStr), &tu)
				outputTokens = tu.CompletionTokens

			case "done":
				if thinkingBlockStarted {
					writeSSE(c, "content_block_stop", ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: thinkingIndex})
				}
				if textBlockStarted {
					writeSSE(c, "content_block_stop", ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: textIndex})
				}
				writeSSE(c, "message_delta", ClaudeStreamMessageDelta{
					Type:  "message_delta",
					Delta: ClaudeMessageDelta{StopReason: "end_turn"},
					Usage: ClaudeUsage{OutputTokens: outputTokens},
				})
				writeSSE(c, "message_stop", map[string]string{"type": "message_stop"})
			}
		}
	}
}

func handleClaudeNonStream(c *gin.Context, body io.ReadCloser, model string) {
	var currentEvent string
	var accumulatedResponse string
	var accumulatedReasoning string
	var inputTokens int
	var outputTokens int

	readTraeSSE(body, func(event string, data string) bool {
		currentEvent = event
		switch currentEvent {
		case "error":
			var traeErr TraeError
			json.Unmarshal([]byte(data), &traeErr)
			c.JSON(http.StatusOK, ClaudeResponse{
				ID:         "msg_" + uuid.New().String(),
				Type:       "message",
				Role:       "assistant",
				Content:    []ClaudeContentBlock{{Type: "text", Text: traeErr.Error}},
				Model:      model,
				StopReason: "end_turn",
			})
			return false
		case "output":
			var out TraeOutput
			json.Unmarshal([]byte(data), &out)
			accumulatedResponse += out.Response
			accumulatedReasoning += out.ReasoningContent
		case "token_usage":
			var tu TraeTokenUsage
			json.Unmarshal([]byte(data), &tu)
			inputTokens = tu.PromptTokens
			outputTokens = tu.CompletionTokens
		}
		return true
	})

	content := []ClaudeContentBlock{}
	if accumulatedReasoning != "" {
		content = append(content, ClaudeContentBlock{
			Type:      "thinking",
			Thinking:  accumulatedReasoning,
			Signature: "ErUB3kr3b0aW5jY2x1ZGVfY2hhbGxlbmdlX3NpZ25hdHVyZV9mYWtlX2Zvcl9wcm94eQ",
		})
	}
	content = append(content, ClaudeContentBlock{Type: "text", Text: accumulatedResponse})

	c.JSON(http.StatusOK, ClaudeResponse{
		ID:         "msg_" + uuid.New().String(),
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      model,
		StopReason: "end_turn",
		Usage:      ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens},
	})
}
