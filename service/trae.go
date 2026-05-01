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
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
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
