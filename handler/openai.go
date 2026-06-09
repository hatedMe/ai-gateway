package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"traecli-api-proxy/config"
	"traecli-api-proxy/logger"
	"traecli-api-proxy/model"
	"traecli-api-proxy/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func HandleModels(c *gin.Context) {
	type ModelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var models []ModelItem
	for name := range config.ModelMap {
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

func HandleChatCompletions(c *gin.Context) {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		auth = c.GetHeader("X-Api-Key")
	}
	auth = strings.TrimPrefix(auth, "Bearer ")
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization or X-Api-Key header is required"})
		return
	}

	rid, _ := c.Get("requestID")
	ctx := logger.WithRequestID(c.Request.Context(), rid.(string))

	var req model.OpenAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	reqJSON, _ := json.MarshalIndent(req, "", "  ")
	timestamp := time.Now().Format("20060102_150405")
	writeLog(fmt.Sprintf("openai_request_%s.json", timestamp), string(reqJSON))

	traeMessages, userInput := service.ConvertOpenAIMessagesToTrae(req.Messages)

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, service.FilterBlockedTools(req.Tools), userInput)

	payloadJSON, _ := json.Marshal(traePayload)
	logger.Info(ctx, "[OpenAI] Sending to Trae", "payload", string(payloadJSON))

	resp, err := service.SendTraeRequest(traePayload, auth)
	if err != nil {
		logger.Error(ctx, "[OpenAI] Request error", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	logger.Info(ctx, "[OpenAI] Response status", "status", resp.Status())

	defer resp.RawBody().Close()
	if req.Stream {
		handleOpenAIStream(c, resp.RawBody(), req.Model, timestamp)
	} else {
		handleOpenAINonStream(c, resp.RawBody(), req.Model, timestamp)
	}
}

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, modelName string, timestamp string) {
	rid, _ := c.Get("requestID")
	ctx := logger.WithRequestID(c.Request.Context(), rid.(string))

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	var rawSSE strings.Builder

	c.Stream(func(w io.Writer) bool {
		service.ReadTraeSSE(body, func(event string, dataStr string) bool {
			rawSSE.WriteString(fmt.Sprintf("event: %s\ndata: %s\n\n", event, dataStr))

			if event == "error" {
				var traeErr model.TraeError
				json.Unmarshal([]byte(dataStr), &traeErr)
				logger.Error(ctx, "[OpenAI] Stream error event", "error", traeErr.Error)
				errChunk := model.OpenAIChunk{
					ID:      "chatcmpl-" + uuid.New().String(),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []model.OpenAIChunkChoice{{Index: 0, Delta: model.OpenAIDelta{Content: traeErr.Error}}},
				}
				j, _ := json.Marshal(errChunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
				w.Write([]byte("data: [DONE]\n\n"))
				return false
			}

			if event == "output" {
				var out model.TraeOutput
				json.Unmarshal([]byte(dataStr), &out)

				if len(out.ToolCalls) > 0 {
					for _, tc := range out.ToolCalls {
						service.AccumulateToolCall(toolCallsAcc, tc)
					}
				}

				delta := model.OpenAIDelta{}
				if out.Response != "" {
					delta.Content = out.Response
				}
				if out.ReasoningContent != "" {
					delta.ReasoningContent = out.ReasoningContent
				}
				if delta.Content != nil || delta.ReasoningContent != nil {
					chunk := model.OpenAIChunk{
						ID:      "chatcmpl-" + uuid.New().String(),
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   modelName,
						Choices: []model.OpenAIChunkChoice{{Index: 0, Delta: delta}},
					}
					j, _ := json.Marshal(chunk)
					w.Write([]byte("data: " + string(j) + "\n\n"))
				}
			}
			return true
		})

		// Send accumulated tool calls and final [DONE]
		if len(toolCallsAcc) > 0 {
			toolCalls := make([]model.OpenAIToolCall, 0)
			for i := 0; i < len(toolCallsAcc); i++ {
				if tc, ok := toolCallsAcc[i]; ok {
					toolCalls = append(toolCalls, model.OpenAIToolCall{
						Index: i,
						ID:    tc.ID,
						Type:  "function",
						Function: model.OpenAIToolCallFunction{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					})
				}
			}
			chunk := model.OpenAIChunk{
				ID:      "chatcmpl-" + uuid.New().String(),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []model.OpenAIChunkChoice{{
					Index:        0,
					Delta:        model.OpenAIDelta{ToolCalls: toolCalls},
					FinishReason: nil,
				}},
			}
			j, _ := json.Marshal(chunk)
			w.Write([]byte("data: " + string(j) + "\n\n"))
		}

		finishChunk := model.OpenAIChunk{
			ID:      "chatcmpl-" + uuid.New().String(),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   modelName,
			Choices: []model.OpenAIChunkChoice{{
				Index:        0,
				Delta:        model.OpenAIDelta{},
				FinishReason: "stop",
			}},
		}
		if len(toolCallsAcc) > 0 {
			finishChunk.Choices[0].FinishReason = "tool_calls"
		}
		j, _ := json.Marshal(finishChunk)
		w.Write([]byte("data: " + string(j) + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		writeLog(fmt.Sprintf("openai_response_%s.log", timestamp), rawSSE.String())
		return false
	})
}

func handleOpenAINonStream(c *gin.Context, body io.ReadCloser, modelName string, timestamp string) {
	rid, _ := c.Get("requestID")
	ctx := logger.WithRequestID(c.Request.Context(), rid.(string))

	var accumulatedResponse string
	var accumulatedReasoning string
	var toolCalls []model.TraeToolCall
	var finishReason string
	var usage model.OpenAIUsage
	var rawSSE strings.Builder

	service.ReadTraeSSE(body, func(event string, data string) bool {
		logger.Debug(ctx, "Read SSE event", "event", event, "data", data)
		rawSSE.WriteString(fmt.Sprintf("event: %s\ndata: %s\n", event, data))
		switch event {
		case "error":
			var traeErr model.TraeError
			json.Unmarshal([]byte(data), &traeErr)
			c.JSON(http.StatusOK, model.OpenAIResponse{
				ID:      "chatcmpl-" + uuid.New().String(),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []model.OpenAIChoice{{Index: 0, Message: model.OpenAIMessage{Role: "assistant", Content: traeErr.Error}, FinishReason: "stop"}},
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
			usage = model.OpenAIUsage{PromptTokens: tu.PromptTokens, CompletionTokens: tu.CompletionTokens, TotalTokens: tu.TotalTokens}
		case "done":
			var doneData model.TraeDone
			json.Unmarshal([]byte(data), &doneData)
			finishReason = doneData.FinishReason
		}
		return true
	})

	writeLog(fmt.Sprintf("openai_response_%s.log", timestamp), rawSSE.String())

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	for _, tc := range toolCalls {
		service.AccumulateToolCall(toolCallsAcc, tc)
	}

	message := model.OpenAIMessage{Role: "assistant", Content: accumulatedResponse}
	if accumulatedReasoning != "" {
		message.ReasoningContent = accumulatedReasoning
	}
	if len(toolCallsAcc) > 0 {
		openaiToolCalls := make([]model.OpenAIToolCall, 0)
		for i := 0; i < len(toolCallsAcc); i++ {
			if tc, ok := toolCallsAcc[i]; ok {
				openaiToolCalls = append(openaiToolCalls, model.OpenAIToolCall{
					Index: i,
					ID:    tc.ID,
					Type:  "function",
					Function: model.OpenAIToolCallFunction{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
		}
		message.ToolCalls = openaiToolCalls
	}

	fReason := "stop"
	if finishReason == "tool_calls" && len(toolCallsAcc) > 0 {
		fReason = "tool_calls"
	}

	c.JSON(http.StatusOK, model.OpenAIResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{{Index: 0, Message: message, FinishReason: fReason}},
		Usage:   usage,
	})
}
