package main

import (
	"fmt"
	"io"
	"log"

	"github.com/go-resty/resty/v2"
)

func main() {
	client := resty.New()

	// Payload structure based on main.go
	traePayload := map[string]interface{}{
		"config_name":     "glm-5.1",
		"conversation_id": "",
		"model_name":      "glm-5__v2",
		"session_id":      "",
		"user_input":      "",
		"messages": []map[string]interface{}{
			{"role": "user", "content": []map[string]string{{"type": "text", "text": "你好"}}},
		},
	}

	auth := "Cloud-IDE-JWT <your-token-here>"

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("User-Agent", "Go-http-client/1.1").
		SetHeader("Host", "console.enterprise.trae.cn").
		SetHeader("X-App-Id", "7b3f9dc2-8a4e-5c6d-2f1b-9e4a3c5b7df0").
		SetHeader("X-Ide-Function", "chat").
		SetHeader("X-Ide-Version-Code", "20260206").
		SetDoNotParseResponse(true).
		SetBody(traePayload).
		Post("https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat")

	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}

	fmt.Printf("Status: %s\n", resp.Status())
	fmt.Printf("Headers: %v\n", resp.Header())

	defer resp.RawBody().Close()
	bodyBytes, err := io.ReadAll(resp.RawBody())
	if err != nil {
		log.Fatalf("Failed to read body: %v", err)
	}
	fmt.Printf("\n===== Full Response Body =====\n%s\n===== End =====\n", string(bodyBytes))
}
