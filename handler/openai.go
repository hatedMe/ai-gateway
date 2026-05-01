package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"traecli-api-proxy/config"
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
	fmt.Printf("[OpenAI] Auth value: %s\n", auth)
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization or X-Api-Key header is required"})
		return
	}

	var req model.OpenAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	traeMessages := make([]model.TraeMessage, len(req.Messages))
	var userInput string
	for i, msg := range req.Messages {
		contentStr := service.ContentToString(msg.Content)
		traeMessages[i] = model.TraeMessage{Role: msg.Role, Content: []model.TraeContent{{Type: "text", Text: contentStr}}}
		if i == len(req.Messages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}

	traePayload := service.BuildTraeRequest(traeMessages, req.Model, req.Tools, userInput)

	payloadJSON, _ := json.Marshal(traePayload)
	fmt.Printf("[OpenAI] Sending to Trae: %s\n", string(payloadJSON))

	resp, err := service.SendTraeRequest(traePayload, auth)
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

func handleOpenAIStream(c *gin.Context, body io.ReadCloser, modelName string) {
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
				var traeErr model.TraeError
				json.Unmarshal([]byte(dataStr), &traeErr)
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

			var chunk model.OpenAIChunk
			chunk.ID = "chatcmpl-" + uuid.New().String()
			chunk.Object = "chat.completion.chunk"
			chunk.Created = time.Now().Unix()
			chunk.Model = modelName

			if currentEvent == "output" {
				var out model.TraeOutput
				json.Unmarshal([]byte(dataStr), &out)
				delta := model.OpenAIDelta{}
				if out.Response != "" {
					delta.Content = out.Response
				}
				if out.ReasoningContent != "" {
					delta.ReasoningContent = out.ReasoningContent
				}
				if out.ToolCalls != nil {
					delta.ToolCalls = out.ToolCalls
				}
				chunk.Choices = []model.OpenAIChunkChoice{{Index: 0, Delta: delta}}
				j, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
			} else if currentEvent == "done" {
				var done model.TraeDone
				json.Unmarshal([]byte(dataStr), &done)
				chunk.Choices = []model.OpenAIChunkChoice{{Index: 0, Delta: model.OpenAIDelta{}, FinishReason: done.FinishReason}}
				j, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(j) + "\n\n"))
			}
		}
		return true
	})
}

func handleOpenAINonStream(c *gin.Context, body io.ReadCloser, modelName string) {
	var currentEvent string
	var accumulatedResponse string
	var accumulatedReasoning string
	var accumulatedToolCalls interface{}
	var usage model.OpenAIUsage

	service.ReadTraeSSE(body, func(event string, data string) bool {
		currentEvent = event
		switch currentEvent {
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
			if out.ToolCalls != nil {
				accumulatedToolCalls = out.ToolCalls
			}
		case "token_usage":
			var tu model.TraeTokenUsage
			json.Unmarshal([]byte(data), &tu)
			usage = model.OpenAIUsage{PromptTokens: tu.PromptTokens, CompletionTokens: tu.CompletionTokens, TotalTokens: tu.TotalTokens}
		}
		return true
	})

	message := model.OpenAIMessage{Role: "assistant", Content: accumulatedResponse}
	if accumulatedReasoning != "" {
		message.ReasoningContent = accumulatedReasoning
	}
	if accumulatedToolCalls != nil {
		message.ToolCalls = accumulatedToolCalls
	}

	c.JSON(http.StatusOK, model.OpenAIResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{{Index: 0, Message: message, FinishReason: "stop"}},
		Usage:   usage,
	})
}
