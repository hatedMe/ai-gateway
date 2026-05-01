package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"traecli-api-proxy/model"
	"traecli-api-proxy/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func HandleClaudeMessages(c *gin.Context) {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		auth = c.GetHeader("X-Api-Key")
	}
	auth = strings.TrimPrefix(auth, "Bearer ")
	fmt.Printf("[Claude] Auth value: %s\n", auth)
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"type": "error", "error": map[string]string{"type": "authentication_error", "message": "Authorization or X-Api-Key header is required"}})
		return
	}

	var req model.ClaudeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	if req.Model == "" {
		req.Model = "claude-3-5-sonnet-latest"
	}

	traeMessages := []model.TraeMessage{}
	if req.System != nil {
		systemStr := service.ContentToString(req.System)
		traeMessages = append(traeMessages, model.TraeMessage{Role: "system", Content: []model.TraeContent{{Type: "text", Text: systemStr}}})
	}

	var userInput string
	for i, msg := range req.Messages {
		contentStr := service.ClaudeContentToString(msg.Content)
		traeMessages = append(traeMessages, model.TraeMessage{Role: msg.Role, Content: []model.TraeContent{{Type: "text", Text: contentStr}}})
		if i == len(req.Messages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, service.ConvertClaudeToolsToTrae(req.Tools), userInput)

	payloadJSON, _ := json.Marshal(traePayload)
	fmt.Printf("[Claude] Sending to Trae: %s\n", string(payloadJSON))

	resp, err := service.SendTraeRequest(traePayload, auth)
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

func writeSSE(c *gin.Context, eventType string, data interface{}) {
	j, _ := json.Marshal(data)
	c.Writer.Write([]byte("event: " + eventType + "\ndata: " + string(j) + "\n\n"))
	c.Writer.Flush()
}

func handleClaudeStream(c *gin.Context, body io.ReadCloser, modelName string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	msgID := "msg_" + uuid.New().String()

	writeSSE(c, "message_start", model.ClaudeStreamMessageStart{
		Type: "message_start",
		Message: model.ClaudeResponse{
			ID:      msgID,
			Type:    "message",
			Role:    "assistant",
			Content: []model.ClaudeContentBlock{},
			Model:   modelName,
			Usage:   model.ClaudeUsage{InputTokens: 0, OutputTokens: 0},
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
				var traeErr model.TraeError
				json.Unmarshal([]byte(dataStr), &traeErr)
				if !textBlockStarted {
					writeSSE(c, "content_block_start", model.ClaudeStreamContentBlockStart{
						Type:         "content_block_start",
						Index:        textIndex,
						ContentBlock: model.ClaudeTextContentBlock{Type: "text", Text: ""},
					})
					textBlockStarted = true
				}
				writeSSE(c, "content_block_delta", model.ClaudeStreamContentBlockDelta{
					Type:  "content_block_delta",
					Index: textIndex,
					Delta: model.ClaudeTextDelta{Type: "text_delta", Text: traeErr.Error},
				})
				writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: textIndex})
				writeSSE(c, "message_delta", model.ClaudeStreamMessageDelta{
					Type:  "message_delta",
					Delta: model.ClaudeMessageDelta{StopReason: "end_turn"},
					Usage: model.ClaudeUsage{OutputTokens: 0},
				})
				writeSSE(c, "message_stop", map[string]string{"type": "message_stop"})
				return

			case "output":
				var out model.TraeOutput
				json.Unmarshal([]byte(dataStr), &out)

				if out.ReasoningContent != "" {
					if !thinkingBlockStarted {
						writeSSE(c, "content_block_start", model.ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        thinkingIndex,
							ContentBlock: model.ClaudeThinkingContentBlock{Type: "thinking", Thinking: ""},
						})
						thinkingBlockStarted = true
					}
					writeSSE(c, "content_block_delta", model.ClaudeStreamContentBlockDelta{
						Type:  "content_block_delta",
						Index: thinkingIndex,
						Delta: model.ClaudeThinkingDelta{Type: "thinking_delta", Thinking: out.ReasoningContent},
					})
				}

				if out.Response != "" {
					if thinkingBlockStarted && !textBlockStarted {
						writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: thinkingIndex})
						writeSSE(c, "content_block_start", model.ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        textIndex,
							ContentBlock: model.ClaudeTextContentBlock{Type: "text", Text: ""},
						})
						textBlockStarted = true
					}
					if !thinkingBlockStarted && !textBlockStarted {
						textIndex = 0
						writeSSE(c, "content_block_start", model.ClaudeStreamContentBlockStart{
							Type:         "content_block_start",
							Index:        textIndex,
							ContentBlock: model.ClaudeTextContentBlock{Type: "text", Text: ""},
						})
						textBlockStarted = true
					}
					writeSSE(c, "content_block_delta", model.ClaudeStreamContentBlockDelta{
						Type:  "content_block_delta",
						Index: textIndex,
						Delta: model.ClaudeTextDelta{Type: "text_delta", Text: out.Response},
					})
				}

			case "token_usage":
				var tu model.TraeTokenUsage
				json.Unmarshal([]byte(dataStr), &tu)
				outputTokens = tu.CompletionTokens

			case "done":
				if thinkingBlockStarted {
					writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: thinkingIndex})
				}
				if textBlockStarted {
					writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: textIndex})
				}
				writeSSE(c, "message_delta", model.ClaudeStreamMessageDelta{
					Type:  "message_delta",
					Delta: model.ClaudeMessageDelta{StopReason: "end_turn"},
					Usage: model.ClaudeUsage{OutputTokens: outputTokens},
				})
				writeSSE(c, "message_stop", map[string]string{"type": "message_stop"})
			}
		}
	}
}

func handleClaudeNonStream(c *gin.Context, body io.ReadCloser, modelName string) {
	var currentEvent string
	var accumulatedResponse string
	var accumulatedReasoning string
	var inputTokens int
	var outputTokens int

	service.ReadTraeSSE(body, func(event string, data string) bool {
		currentEvent = event
		switch currentEvent {
		case "error":
			var traeErr model.TraeError
			json.Unmarshal([]byte(data), &traeErr)
			c.JSON(http.StatusOK, model.ClaudeResponse{
				ID:         "msg_" + uuid.New().String(),
				Type:       "message",
				Role:       "assistant",
				Content:    []model.ClaudeContentBlock{{Type: "text", Text: traeErr.Error}},
				Model:      modelName,
				StopReason: "end_turn",
			})
			return false
		case "output":
			var out model.TraeOutput
			json.Unmarshal([]byte(data), &out)
			accumulatedResponse += out.Response
			accumulatedReasoning += out.ReasoningContent
		case "token_usage":
			var tu model.TraeTokenUsage
			json.Unmarshal([]byte(data), &tu)
			inputTokens = tu.PromptTokens
			outputTokens = tu.CompletionTokens
		}
		return true
	})

	content := []model.ClaudeContentBlock{}
	if accumulatedReasoning != "" {
		content = append(content, model.ClaudeContentBlock{
			Type:      "thinking",
			Thinking:  accumulatedReasoning,
			Signature: "ErUB3kr3b0aW5jY2x1ZGVfY2hhbGxlbmdlX3NpZ25hdHVyZV9mYWtlX2Zvcl9wcm94eQ",
		})
	}
	content = append(content, model.ClaudeContentBlock{Type: "text", Text: accumulatedResponse})

	c.JSON(http.StatusOK, model.ClaudeResponse{
		ID:         "msg_" + uuid.New().String(),
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      modelName,
		StopReason: "end_turn",
		Usage:      model.ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens},
	})
}
