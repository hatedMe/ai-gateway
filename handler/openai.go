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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	appkey := c.GetHeader("Authorization")
	if appkey == "" {
		appkey = c.GetHeader("X-Api-Key")
	}
	appkey = strings.TrimPrefix(appkey, "Bearer ")
	if appkey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization or X-Api-Key header is required"})
		return
	}

	auth := config.GetRealToken(appkey)
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid appkey"})
		return
	}

	rid, _ := c.Get("requestID")
	ctx := logger.WithRequestID(c.Request.Context(), rid.(string))

	var req model.OpenAIRequest
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}
	writeLog(fmt.Sprintf("openai_request_%s.json", rid), string(rawBody))
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	logger.Info(ctx, "[OpenAI] Response status", "status", resp.Status())

	defer resp.RawBody().Close()
	if req.Stream {
		handleOpenAIStream(c, resp.RawBody(), req.Model, rid.(string))
	} else {
		handleOpenAINonStream(c, resp.RawBody(), req.Model)
	}
}

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, modelName string, requestID string) {
	rid, _ := c.Get("requestID")
	ctx := logger.WithRequestID(c.Request.Context(), rid.(string))

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	toolCallsAcc := map[int]*model.AccumulatedToolCall{}
	var rawSSELog strings.Builder

	c.Stream(func(w io.Writer) bool {
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

		writeLog(fmt.Sprintf("openai_raw_sse_%s.log", requestID), rawSSELog.String())
		return false
	})
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
