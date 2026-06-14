package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"traecli-api-proxy/logger"
	"traecli-api-proxy/model"
	"traecli-api-proxy/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func writeLog(filename string, content string) {
	logsDir := "logs"
	os.MkdirAll(logsDir, 0755)
	f, err := os.Create(filepath.Join(logsDir, filename))
	if err != nil {
		fmt.Printf("[Log] Failed to create log file: %v\n", err)
		return
	}
	defer f.Close()
	f.WriteString(content)
}

func HandleClaudeMessages(c *gin.Context) {
	auth := GetAuthFromContext(c)
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"type": "error", "error": map[string]string{"type": "authentication_error", "message": "Unauthorized"}})
		return
	}

	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	var req model.ClaudeRequest
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": "failed to read request body"}})
		return
	}
	writeLog(fmt.Sprintf("claude_request_%s.json", rid), string(rawBody))
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	if req.Model == "" {
		req.Model = "claude-3-5-sonnet-latest"
	}

	logger.Info(ctx, "[Claude] Request received", "model", req.Model, "stream", req.Stream, "messages_count", len(req.Messages), "tools_count", len(req.Tools))

	traeMessages, userInput := service.ConvertClaudeMessagesToTrae(req.Messages, req.System)

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, service.ConvertClaudeToolsToTrae(req.Tools), userInput)

	payloadJSON, _ := json.MarshalIndent(traePayload, "", "  ")
	writeLog(fmt.Sprintf("claude_payload_%s.json", rid), string(payloadJSON))

	resp, err := service.SendTraeRequest(traePayload, auth)
	if err != nil {
		logger.Error(ctx, "[Claude] Request error", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"type": "error", "error": map[string]string{"type": "api_error", "message": err.Error()}})
		return
	}
	logger.Info(ctx, "[Claude] Response status", "status", resp.Status())

	defer resp.RawBody().Close()
	if resp.StatusCode() != 200 {
		bodyBytes, _ := io.ReadAll(resp.RawBody())
		logger.Error(ctx, "[Claude] Error response body", "body", string(bodyBytes))
		c.JSON(resp.StatusCode(), gin.H{"type": "error", "error": map[string]string{"type": "api_error", "message": string(bodyBytes)}})
		return
	}
	if req.Stream {
		handleClaudeStream(c, resp.RawBody(), req.Model)
	} else {
		handleClaudeNonStream(c, resp.RawBody(), req.Model)
	}
}

func writeSSE(c *gin.Context, eventType string, data any) {
	j, _ := json.Marshal(data)
	c.Writer.Write([]byte("event: " + eventType + "\ndata: " + string(j) + "\n\n"))
	c.Writer.Flush()
}

func handleClaudeStream(c *gin.Context, body io.ReadCloser, modelName string) {
	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

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
	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	nextBlockIndex := 2

	service.ReadTraeSSE(body, func(event string, dataStr string) bool {

		switch event {
		case "error":
			var traeErr model.TraeError
			json.Unmarshal([]byte(dataStr), &traeErr)
			logger.Error(ctx, "[Claude] Stream error event", "error", traeErr.Error)
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
			writeSSE(c, "message_stop", map[string]any{"type": "message_stop"})
			return false

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
					nextBlockIndex = 1
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

			if len(out.ToolCalls) > 0 {
				for _, tc := range out.ToolCalls {
					service.AccumulateToolCall(toolCallsAcc, tc)
				}
			}

		case "token_usage":
			var tu model.TraeTokenUsage
			json.Unmarshal([]byte(dataStr), &tu)
			outputTokens = tu.CompletionTokens

		case "done":
			var doneData model.TraeDone
			json.Unmarshal([]byte(dataStr), &doneData)

			if thinkingBlockStarted {
				writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: thinkingIndex})
			}
			if textBlockStarted {
				writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: textIndex})
			}

			if len(toolCallsAcc) > 0 {
				for i := 0; i < len(toolCallsAcc); i++ {
					if tc, ok := toolCallsAcc[i]; ok {
						logger.Info(ctx, "[Claude] Tool call", "id", tc.ID, "name", tc.Name, "arguments", tc.Arguments)
						toolIndex := nextBlockIndex
						nextBlockIndex++
						writeSSE(c, "content_block_start", model.ClaudeStreamContentBlockStart{
							Type:  "content_block_start",
							Index: toolIndex,
							ContentBlock: model.ClaudeToolUseContentBlock{
								Type:  "tool_use",
								ID:    tc.ID,
								Name:  tc.Name,
								Input: map[string]any{},
							},
						})
						writeSSE(c, "content_block_delta", model.ClaudeStreamContentBlockDelta{
							Type:  "content_block_delta",
							Index: toolIndex,
							Delta: model.ClaudeInputJsonDelta{
								Type:        "input_json_delta",
								PartialJson: tc.Arguments,
							},
						})
						writeSSE(c, "content_block_stop", model.ClaudeStreamContentBlockStop{Type: "content_block_stop", Index: toolIndex})
					}
				}
			}

			stopReason := "end_turn"
			if doneData.FinishReason == "tool_calls" && len(toolCallsAcc) > 0 {
				stopReason = "tool_use"
			}
			writeSSE(c, "message_delta", model.ClaudeStreamMessageDelta{
				Type:  "message_delta",
				Delta: model.ClaudeMessageDelta{StopReason: stopReason},
				Usage: model.ClaudeUsage{OutputTokens: outputTokens},
			})
			writeSSE(c, "message_stop", map[string]any{"type": "message_stop"})
		}
		if event == "done" {
			return false
		}
		return true
	})
}

func handleClaudeNonStream(c *gin.Context, body io.ReadCloser, modelName string) {
	var accumulatedResponse string
	var accumulatedReasoning string
	var inputTokens int
	var outputTokens int
	var toolCalls []model.TraeToolCall
	var finishReason string

	service.ReadTraeSSE(body, func(event string, data string) bool {
		switch event {
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
			if len(out.ToolCalls) > 0 {
				toolCalls = append(toolCalls, out.ToolCalls...)
			}
		case "token_usage":
			var tu model.TraeTokenUsage
			json.Unmarshal([]byte(data), &tu)
			inputTokens = tu.PromptTokens
			outputTokens = tu.CompletionTokens
		case "done":
			var doneData model.TraeDone
			json.Unmarshal([]byte(data), &doneData)
			finishReason = doneData.FinishReason
		}
		return true
	})

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	for _, tc := range toolCalls {
		service.AccumulateToolCall(toolCallsAcc, tc)
	}

	stopReason := "end_turn"
	if finishReason == "tool_calls" && len(toolCallsAcc) > 0 {
		stopReason = "tool_use"
	}

	content := []model.ClaudeContentBlock{}
	if accumulatedReasoning != "" {
		content = append(content, model.ClaudeContentBlock{
			Type:      "thinking",
			Thinking:  accumulatedReasoning,
			Signature: "ErUB3kr3b0aW5jY2x1ZGVfY2hhbGxlbmdlX3NpZ25hdHVyZV9mYWtlX2Zvcl9wcm94eQ",
		})
	}
	if accumulatedResponse != "" {
		content = append(content, model.ClaudeContentBlock{Type: "text", Text: accumulatedResponse})
	}
	for i := 0; i < len(toolCallsAcc); i++ {
		if tc, ok := toolCallsAcc[i]; ok {
			var input map[string]any
			json.Unmarshal([]byte(tc.Arguments), &input)
			content = append(content, model.ClaudeContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
	}

	c.JSON(http.StatusOK, model.ClaudeResponse{
		ID:         "msg_" + uuid.New().String(),
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      modelName,
		StopReason: stopReason,
		Usage:      model.ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens},
	})
}



