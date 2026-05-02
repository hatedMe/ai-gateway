package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"traecli-api-proxy/config"
	"traecli-api-proxy/model"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

func BuildTraeRequest(traeMessages []model.TraeMessage, modelName string, tools []interface{}, userInput string) model.TraeRequest {
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

func SendTraeRequest(traePayload model.TraeRequest, auth string) (*resty.Response, error) {
	if config.Cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}

	if auth == "" {
		return nil, fmt.Errorf("authorization header is required")
	}

	req := resty.New().R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("User-Agent", "Go-http-client/1.1").
		SetHeader("Host", "console.enterprise.trae.cn").
		SetBody(traePayload).
		SetDoNotParseResponse(true)

	for k, v := range config.Cfg.Trae.Headers {
		req = req.SetHeader(k, v)
	}

	return req.Post(config.TargetURL)
}

func ContentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

func ClaudeContentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if block, ok := item.(map[string]interface{}); ok {
				switch block["type"] {
				case "text":
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				case "tool_use":
					name, _ := block["name"].(string)
					id, _ := block["id"].(string)
					inputJSON, _ := json.Marshal(block["input"])
					parts = append(parts, fmt.Sprintf("[Tool Use: %s (id: %s) Input: %s]", name, id, string(inputJSON)))
				case "tool_result":
					id, _ := block["tool_use_id"].(string)
					var resultContent string
					if rc, ok := block["content"].([]interface{}); ok {
						var rcParts []string
						for _, rcItem := range rc {
							if rcBlock, ok := rcItem.(map[string]interface{}); ok {
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
					errMark := ""
					if isError {
						errMark = " (ERROR)"
					}
					parts = append(parts, fmt.Sprintf("[Tool Result%s (id: %s): %s]", errMark, id, resultContent))
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

func FilterBlockedTools(tools []interface{}) []interface{} {
	return tools
}

func OpenAIContentToString(content interface{}, toolCalls interface{}, role string) string {
	var parts []string

	if toolCalls != nil {
		switch tc := toolCalls.(type) {
		case []interface{}:
			for _, item := range tc {
				if m, ok := item.(map[string]interface{}); ok {
					fn, _ := m["function"].(map[string]interface{})
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					id, _ := m["id"].(string)
					parts = append(parts, fmt.Sprintf("[Tool Use: %s (id: %s) Input: %s]", name, id, args))
				}
			}
		case []model.OpenAIToolCall:
			for _, tcItem := range tc {
				parts = append(parts, fmt.Sprintf("[Tool Use: %s (id: %s) Input: %s]", tcItem.Function.Name, tcItem.ID, tcItem.Function.Arguments))
			}
		}
	}

	if content != nil {
		switch v := content.(type) {
		case string:
			if v != "" {
				parts = append(parts, v)
			}
		default:
			j, _ := json.Marshal(v)
			if string(j) != "null" && string(j) != `""` {
				parts = append(parts, string(j))
			}
		}
	}

	return strings.Join(parts, "\n")
}

func ConvertClaudeToolsToTrae(claudeTools []interface{}) []interface{} {
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
