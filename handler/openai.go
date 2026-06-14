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

func abortWithOpenAIError(c *gin.Context, statusCode int, message string, errorType string, code string) {
	c.JSON(statusCode, gin.H{
		"error": map[string]string{
			"message": message,
			"type":    errorType,
			"code":    code,
		},
	})
	c.Abort()
}

func HandleModels(c *gin.Context) {
	auth := GetAuthFromContext(c)
	if auth == "" {
		abortWithOpenAIError(c, http.StatusUnauthorized, "Unauthorized", "authentication_error", "")
		return
	}

	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	type ModelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var models []ModelItem

	traeConfigs, err := service.FetchTraeModels(auth)
	if err != nil {
		logger.Error(ctx, "[OpenAI] Fetch models error", "error", err)
		abortWithOpenAIError(c, http.StatusInternalServerError, err.Error(), "api_error", "")
		return
	}

	for _, cfg := range traeConfigs {
		if cfg.Usage == "chat_completion" {
			models = append(models, ModelItem{
				ID:      cfg.ConfigName,
				Object:  "model",
				Created: 1750118400,
				OwnedBy: "trae",
			})
		}
	}

	if len(models) == 0 {
		for name := range config.ModelMap {
			models = append(models, ModelItem{
				ID:      name,
				Object:  "model",
				Created: 1750118400,
				OwnedBy: "trae",
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

func HandleChatCompletions(c *gin.Context) {
	auth := GetAuthFromContext(c)
	if auth == "" {
		abortWithOpenAIError(c, http.StatusUnauthorized, "Unauthorized", "authentication_error", "")
		return
	}

	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	var req model.OpenAIRequest
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		abortWithOpenAIError(c, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "")
		return
	}
	writeLog(fmt.Sprintf("openai_request_%s.json", rid), string(rawBody))
	if err := json.Unmarshal(rawBody, &req); err != nil {
		abortWithOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}

	if req.Model == "" {
		abortWithOpenAIError(c, http.StatusBadRequest, "model is required", "invalid_request_error", "")
		return
	}

	logger.Info(ctx, "[OpenAI] Request received", "model", req.Model, "stream", req.Stream, "messages_count", len(req.Messages), "tools_count", len(req.Tools))

	traeMessages, userInput := service.ConvertOpenAIMessagesToTrae(req.Messages)

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, service.ConvertClaudeToolsToTrae(service.FilterBlockedTools(req.Tools)), userInput)

	payloadJSON, _ := json.MarshalIndent(traePayload, "", "  ")
	writeLog(fmt.Sprintf("openai_payload_%s.json", rid), string(payloadJSON))

	resp, err := service.SendTraeRequest(traePayload, auth)
	if err != nil {
		logger.Error(ctx, "[OpenAI] Request error", "error", err)
		abortWithOpenAIError(c, http.StatusInternalServerError, err.Error(), "api_error", "")
		return
	}
	logger.Info(ctx, "[OpenAI] Response status", "status", resp.Status())

	defer resp.RawBody().Close()
	if req.Stream {
		handleOpenAIStream(c, resp.RawBody(), req.Model, rid)
	} else {
		handleOpenAINonStream(c, resp.RawBody(), req.Model)
	}
}

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, modelName string, requestID string) {
	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	var rawSSELog strings.Builder

	flusher, ok := c.Writer.(http.Flusher)
	
	service.ReadTraeSSE(body, func(event string, dataStr string) bool {
		rawSSELog.WriteString(fmt.Sprintf("event: %s\ndata: %s\n\n", event, dataStr))

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
				c.Writer.Write([]byte("data: " + string(j) + "\n\n"))
				if ok {
					flusher.Flush()
				}
				c.Writer.Write([]byte("data: [DONE]\n\n"))
				if ok {
					flusher.Flush()
				}
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
					c.Writer.Write([]byte("data: " + string(j) + "\n\n"))
				if ok {
					flusher.Flush()
				}
				}
			}

			if event == "done" {
				return false
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
			c.Writer.Write([]byte("data: " + string(j) + "\n\n"))
				if ok {
					flusher.Flush()
				}
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
		c.Writer.Write([]byte("data: " + string(j) + "\n\n"))
				if ok {
					flusher.Flush()
				}
		c.Writer.Write([]byte("data: [DONE]\n\n"))
				if ok {
					flusher.Flush()
				}
	
	writeLog(fmt.Sprintf("openai_raw_sse_%s.log", requestID), rawSSELog.String())
}

func handleOpenAINonStream(c *gin.Context, body io.ReadCloser, modelName string) {
	var accumulatedResponse string
	var accumulatedReasoning string
	var toolCalls []model.TraeToolCall
	var finishReason string
	var usage model.OpenAIUsage

	service.ReadTraeSSE(body, func(event string, data string) bool {
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






