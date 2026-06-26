package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"traecli-api-proxy/config"
	"traecli-api-proxy/model"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

var (
	httpClient *resty.Client
	clientOnce sync.Once
)

func GetHTTPClient() *resty.Client {
	clientOnce.Do(func() {
		timeout := 300
		maxIdle := 100
		retry := 2
		if config.Cfg != nil && config.Cfg.HTTP.TimeoutSeconds > 0 {
			timeout = config.Cfg.HTTP.TimeoutSeconds
			maxIdle = config.Cfg.HTTP.MaxIdleConns
			retry = config.Cfg.HTTP.RetryCount
		}

		transport := &http.Transport{
			MaxIdleConns:        maxIdle,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		}
		httpClient = resty.New().
			SetTimeout(time.Duration(timeout)*time.Second).
			SetHeader("User-Agent", "Go-http-client/1.1").
			SetHeader("Host", "console.enterprise.trae.cn").
			SetDoNotParseResponse(true).
			SetTransport(transport).
			SetRetryCount(retry).
			SetRetryWaitTime(500 * time.Millisecond).
			SetRetryMaxWaitTime(2 * time.Second)
	})
	return httpClient
}

func BuildTraeRequest(traeMessages []model.TraeMessage, modelName string, tools []any, userInput string) model.TraeRequest {
	modelCfg := config.GetModelConfig(modelName)
	return model.TraeRequest{
		ConfigName:     modelCfg.ConfigName,
		ConversationID: uuid.New().String(),
		Messages:       traeMessages,
		ModelName:      modelCfg.ModelName,
		SessionID:      uuid.New().String(),
		Tools:          tools,
		UserInput:      userInput,
	}
}

func AccumulateToolCall(acc map[int]*model.AccumulatedToolCall, tc model.TraeToolCall) {
	idx := tc.Index
	if tc.ID != "" {
		acc[idx] = &model.AccumulatedToolCall{
			ID:        tc.ID,
			Name:      tc.FunctionCall.Name,
			Arguments: tc.FunctionCall.Arguments,
		}
	} else if existing, ok := acc[idx]; ok {
		existing.Arguments += tc.FunctionCall.Arguments
		if tc.FunctionCall.Name != "" {
			existing.Name = tc.FunctionCall.Name
		}
	}
}

func SendTraeRequest(traePayload model.TraeRequest, auth string) (*resty.Response, error) {
	// Access configuration using mutex if exported, otherwise use local copy
	// This relies on the concurrency fixes applied in config/config.go
	if config.Cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}

	if auth == "" {
		return nil, fmt.Errorf("authorization header is required")
	}

	req := GetHTTPClient().R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Authorization", auth).
		SetBody(traePayload)

	for k, v := range config.Cfg.Trae.Headers {
		req = req.SetHeader(k, v)
	}

	return req.Post(config.TargetURL)
}

func ContentToString(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

func ConvertClaudeToolsToTrae(claudeTools []any) []any {
	var traeTools []any
	for _, tool := range claudeTools {
		if m, ok := tool.(map[string]any); ok {
			if m["type"] == "function" {
				if fn, ok := m["function"].(map[string]any); ok {
					if params, ok := fn["parameters"]; ok {
						if _, isStr := params.(string); !isStr {
							schemaBytes, _ := json.Marshal(params)
							fn["parameters"] = string(schemaBytes)
						}
					}
					traeTools = append(traeTools, m)
					continue
				}
			}
			name, _ := m["name"].(string)
			desc, _ := m["description"].(string)
			inputSchema := m["input_schema"]
			if name != "" {
				// input_schema is expected to be a JSON object, but Trae expects parameters as a JSON string
				schemaBytes, _ := json.Marshal(inputSchema)
				traeTool := map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        name,
						"description": desc,
						"parameters":  string(schemaBytes),
					},
				}
				traeTools = append(traeTools, traeTool)
			}
		}
	}
	return traeTools
}

func ReadTraeSSE(ctx context.Context, body io.ReadCloser, onEvent func(event string, data string) bool) {
	reader := bufio.NewReaderSize(body, 64*1024)
	var currentEvent string
	for {
		if ctx != nil && ctx.Err() != nil {
			return
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if s, ok := strings.CutPrefix(line, "event:"); ok {
			currentEvent = strings.TrimSpace(s)
		} else if s, ok := strings.CutPrefix(line, "data:"); ok {
			dataStr := strings.TrimSpace(s)
			if dataStr == "" {
				continue
			}
			if !onEvent(currentEvent, dataStr) {
				return
			}
		}
	}
}

func ConvertOpenAIMessagesToTrae(openaiMessages []model.OpenAIMessage) ([]model.TraeMessage, string) {
	traeMessages := make([]model.TraeMessage, len(openaiMessages))
	var userInput string
	for i, msg := range openaiMessages {
		traeMsg := model.TraeMessage{Role: msg.Role}

		contentStr := ""
		if msg.Content != nil {
			switch v := msg.Content.(type) {
			case string:
				contentStr = v
			default:
				j, _ := json.Marshal(v)
				if string(j) != "null" && string(j) != `""` {
					contentStr = string(j)
				}
			}
		}
		traeMsg.Content = []model.TraeContent{{Type: "text", Text: contentStr}}

		if msg.ToolCalls != nil {
			switch tc := msg.ToolCalls.(type) {
			case []any:
				for _, item := range tc {
					if m, ok := item.(map[string]any); ok {
						fn, _ := m["function"].(map[string]any)
						name, _ := fn["name"].(string)
						args, _ := fn["arguments"].(string)
						id, _ := m["id"].(string)
						traeMsg.ToolCalls = append(traeMsg.ToolCalls, map[string]any{
							"id":   id,
							"type": "function",
							"function": map[string]any{
								"name":      name,
								"arguments": args,
							},
						})
					}
				}
			case []model.OpenAIToolCall:
				for _, tcItem := range tc {
					traeMsg.ToolCalls = append(traeMsg.ToolCalls, map[string]any{
						"id":   tcItem.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tcItem.Function.Name,
							"arguments": tcItem.Function.Arguments,
						},
					})
				}
			}
		}

		if msg.ToolCallID != "" {
			traeMsg.ToolCallID = msg.ToolCallID
		}

		traeMessages[i] = traeMsg
		if i == len(openaiMessages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}
	return traeMessages, userInput
}

func ConvertClaudeMessagesToTrae(claudeMessages []model.ClaudeMessage, system any) ([]model.TraeMessage, string, error) {
	traeMessages := []model.TraeMessage{}
	if system != nil {
		systemStr := ContentToString(system)
		traeMessages = append(traeMessages, model.TraeMessage{Role: "system", Content: []model.TraeContent{{Type: "text", Text: systemStr}}})
	}

	var userInput string
	for i, msg := range claudeMessages {
		traeMsg, err := parseClaudeMessage(msg)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse message at index %d: %w", i, err)
		}

		traeMessages = append(traeMessages, traeMsg)
		if i == len(claudeMessages)-1 && msg.Role == "user" {
			// Extract user input from text content, assuming it's the first text block for now
			// Or more robustly extract from the parsed content
			userInput = traeMsg.Content[0].Text
		}
	}
	return traeMessages, userInput, nil
}

func parseClaudeMessage(msg model.ClaudeMessage) (model.TraeMessage, error) {
	traeMsg := model.TraeMessage{Role: msg.Role}
	contentStr := ""

	switch v := msg.Content.(type) {
	case string:
		contentStr = v
	case []any:
		var parts []string
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				return traeMsg, fmt.Errorf("invalid content block type")
			}

			switch block["type"] {
			case "text":
				text, ok := block["text"].(string)
				if !ok {
					return traeMsg, fmt.Errorf("invalid text block")
				}
				parts = append(parts, text)

			case "tool_use":
				name, _ := block["name"].(string)
				id, _ := block["id"].(string)
				inputJSON, err := json.Marshal(block["input"])
				if err != nil {
					return traeMsg, fmt.Errorf("failed to marshal tool input: %w", err)
				}

				if traeMsg.ToolCalls == nil {
					traeMsg.ToolCalls = make([]map[string]any, 0)
				}
				traeMsg.ToolCalls = append(traeMsg.ToolCalls, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": string(inputJSON),
					},
				})
			case "tool_result":
				traeMsg.Role = "tool"
				id, _ := block["tool_use_id"].(string)
				traeMsg.ToolCallID = id

				resultContent, err := parseToolResultContent(block["content"])
				if err != nil {
					return traeMsg, fmt.Errorf("failed to parse tool result: %w", err)
				}

				isError, _ := block["is_error"].(bool)
				if isError {
					resultContent = "ERROR: " + resultContent
				}
				parts = append(parts, resultContent)
			}
		}
		contentStr = strings.Join(parts, "\n")
	default:
		return traeMsg, fmt.Errorf("unsupported content type: %T", v)
	}

	traeMsg.Content = []model.TraeContent{{Type: "text", Text: contentStr}}
	return traeMsg, nil
}

func parseToolResultContent(content any) (string, error) {
	switch rc := content.(type) {
	case []any:
		var rcParts []string
		for _, rcItem := range rc {
			rcBlock, ok := rcItem.(map[string]any)
			if !ok {
				continue
			}
			if rcBlock["type"] == "text" {
				if text, ok := rcBlock["text"].(string); ok {
					rcParts = append(rcParts, text)
				}
			}
		}
		return strings.Join(rcParts, "\n"), nil
	case string:
		return rc, nil
	default:
		return "", fmt.Errorf("unsupported tool result content type: %T", content)
	}
}
