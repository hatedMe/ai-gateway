package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"traecli-api-proxy/service"
)

func main() {
	// 模拟 Claude 发送的 tools 格式
	claudeTools := []interface{}{
		map[string]interface{}{
			"name":        "get_weather",
			"description": "Get current weather for a city",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}

	fmt.Println("=== 原始 Claude 格式 ===")
	claudeJSON, _ := json.MarshalIndent(claudeTools, "", "  ")
	fmt.Println(string(claudeJSON))

	// 调用转换函数
	traeTools := service.ConvertClaudeToolsToTrae(claudeTools)

	fmt.Println("\n=== 转换后的 Trae 格式 ===")
	traeJSON, _ := json.MarshalIndent(traeTools, "", "  ")
	// 检查 parameters 是否成功转成了字符串
	traeJSONStr := string(traeJSON)
	fmt.Println(traeJSONStr)
	
	if strings.Contains(traeJSONStr, "\"parameters\": \"{\\\"properties\\\":") {
		fmt.Println("\n✅ 测试通过：parameters 已成功转为 JSON 字符串。")
	} else {
		fmt.Println("\n❌ 测试失败：parameters 仍然是对象格式，或者转换出错。")
	}
}
