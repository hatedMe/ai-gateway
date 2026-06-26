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

	traeConfigs, err := service.FetchTraeModels(ctx, auth)
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
	rawBody, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20))
	if err != nil {
		abortWithOpenAIError(c, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "")
		return
	}
	writeLog(ctx, fmt.Sprintf("openai_request_%s.json", rid), rawBody)
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

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, service.ConvertClaudeToolsToTrae(req.Tools), userInput)

	payloadJSON, _ := json.MarshalIndent(traePayload, "", "  ")
	writeLog(ctx, fmt.Sprintf("openai_payload_%s.json", rid), payloadJSON)

	resp, err := service.SendTraeRequest(traePayload, auth)
	if err != nil {
		logger.Error(ctx, "[OpenAI] Request error", "error", err)
		abortWithOpenAIError(c, http.StatusInternalServerError, err.Error(), "api_error", "")
		return
	}
	logger.Info(ctx, "[OpenAI] Response status", "status", resp.Status())

	defer resp.RawBody().Close()
	if resp.StatusCode() != 200 {
		bodyBytes, _ := io.ReadAll(resp.RawBody())
		logger.Error(ctx, "[OpenAI] Error response body", "status", resp.StatusCode(), "body", string(bodyBytes))
		abortWithOpenAIError(c, resp.StatusCode(), fmt.Sprintf("upstream returned status %d", resp.StatusCode()), "api_error", "")
		return
	}
	if req.Stream {
		handleOpenAIStream(c, resp.RawBody(), req.Model)
	} else {
		handleOpenAINonStream(c, resp.RawBody(), req.Model)
	}
}

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, modelName string) {
	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// 同一响应内所有 chunk 共享同一 ID（OpenAI 协议要求）
	chunkID := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	var rawSSELog strings.Builder

	flusher, ok := c.Writer.(http.Flusher)

	service.ReadTraeSSE(ctx, body, func(event string, dataStr string) bool {
		// 客户端断开则停止读取上游，避免空转浪费 token
		if c.Request.Context().Err() != nil {
			return false
		}

		fmt.Fprintf(&rawSSELog, "event: %s\ndata: %s\n\n", event, dataStr)

		if event == "error" {
			var traeErr model.TraeError
			if err := json.Unmarshal([]byte(dataStr), &traeErr); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal stream error event failed", "error", err, "data", dataStr)
			}
			logger.Error(ctx, "[OpenAI] Stream error event", "error", traeErr.Error)
			errChunk := model.OpenAIChunk{
				ID:      chunkID,
				Object:  "chat.completion.chunk",
				Created: created,
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
			if err := json.Unmarshal([]byte(dataStr), &out); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal stream output event failed", "error", err, "data", dataStr)
			}

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
					ID:      chunkID,
					Object:  "chat.completion.chunk",
					Created: created,
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
			ID:      chunkID,
			Object:  "chat.completion.chunk",
			Created: created,
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
		ID:      chunkID,
		Object:  "chat.completion.chunk",
		Created: created,
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

	writeLog(ctx, fmt.Sprintf("openai_raw_sse_%s.log", rid), []byte(rawSSELog.String()))
}

func handleOpenAINonStream(c *gin.Context, body io.ReadCloser, modelName string) {
	rid := GetRequestID(c)
	ctx := logger.WithRequestID(c.Request.Context(), rid)

	var respBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []model.TraeToolCall
	var finishReason string
	var usage model.OpenAIUsage

	service.ReadTraeSSE(ctx, body, func(event string, data string) bool {
		switch event {
		case "error":
			var traeErr model.TraeError
			if err := json.Unmarshal([]byte(data), &traeErr); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal nonstream error event failed", "error", err, "data", data)
			}
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
			if err := json.Unmarshal([]byte(data), &out); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal nonstream output event failed", "error", err, "data", data)
			}
			respBuilder.WriteString(out.Response)
			reasoningBuilder.WriteString(out.ReasoningContent)
			if len(out.ToolCalls) > 0 {
				toolCalls = append(toolCalls, out.ToolCalls...)
			}
		case "token_usage":
			var tu model.TraeTokenUsage
			if err := json.Unmarshal([]byte(data), &tu); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal nonstream token_usage event failed", "error", err, "data", data)
			}
			usage = model.OpenAIUsage{PromptTokens: tu.PromptTokens, CompletionTokens: tu.CompletionTokens, TotalTokens: tu.TotalTokens}
		case "done":
			var doneData model.TraeDone
			if err := json.Unmarshal([]byte(data), &doneData); err != nil {
				logger.Warn(ctx, "[OpenAI] unmarshal nonstream done event failed", "error", err, "data", data)
			}
			finishReason = doneData.FinishReason
		}
		return true
	})

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	for _, tc := range toolCalls {
		service.AccumulateToolCall(toolCallsAcc, tc)
	}

	accumulatedResponse := respBuilder.String()
	accumulatedReasoning := reasoningBuilder.String()

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
