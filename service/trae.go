package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
		httpClient = resty.New().
			SetTimeout(120*time.Second).
			SetHeader("User-Agent", "Go-http-client/1.1").
			SetHeader("Host", "console.enterprise.trae.cn").
			SetDoNotParseResponse(true)
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

func ReadTraeSSE(body io.ReadCloser, onEvent func(event string, data string) bool) {
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

func ConvertClaudeMessagesToTrae(claudeMessages []model.ClaudeMessage, system any) ([]model.TraeMessage, string) {
	traeMessages := []model.TraeMessage{}
	if system != nil {
		systemStr := ContentToString(system)
		traeMessages = append(traeMessages, model.TraeMessage{Role: "system", Content: []model.TraeContent{{Type: "text", Text: systemStr}}})
	}

	var userInput string
	for i, msg := range claudeMessages {
		traeMsg := model.TraeMessage{Role: msg.Role}

		contentStr := ""
		switch v := msg.Content.(type) {
		case string:
			contentStr = v
		case []any:
			var parts []string
			for _, item := range v {
				if block, ok := item.(map[string]any); ok {
					switch block["type"] {
					case "text":
						if text, ok := block["text"].(string); ok {
							parts = append(parts, text)
						}
					case "tool_use":
						name, _ := block["name"].(string)
						id, _ := block["id"].(string)
						inputJSON, _ := json.Marshal(block["input"])

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

						var resultContent string
						if rc, ok := block["content"].([]any); ok {
							var rcParts []string
							for _, rcItem := range rc {
								if rcBlock, ok := rcItem.(map[string]any); ok {
									if rcBlock["type"] == "text" {
										if text, ok := rcBlock["text"].(string); ok {
											rcParts = append(rcParts, text)
										}
									}
								}
							}
							resultContent = strings.Join(rcParts, "\n")
						} else if text, ok := block["content"].(string); ok {
							resultContent = text
						}

						isError, _ := block["is_error"].(bool)
						if isError {
							resultContent = "ERROR: " + resultContent
						}
						parts = append(parts, resultContent)
					}
				}
			}
			contentStr = strings.Join(parts, "\n")
		}

		traeMsg.Content = []model.TraeContent{{Type: "text", Text: contentStr}}

		traeMessages = append(traeMessages, traeMsg)
		if i == len(claudeMessages)-1 && msg.Role == "user" {
			userInput = contentStr
		}
	}
	return traeMessages, userInput
}
