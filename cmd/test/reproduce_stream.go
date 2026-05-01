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

	auth := "Cloud-IDE-JWT eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJkYXRhIjp7ImlkIjoiMzA4NzgwMjg4Iiwic2NvcGUiOiJzYWFzIiwic291cmNlIjoicmVmcmVzaF90b2tlbiIsInNvdXJjZV9pZCI6InkxVTR5QUZvM2l1a3p1dExUN2N6NFozLTdHbVBSdEFYTkdPVDN5aFRVV0E9LjE4YWI2NmRhMGE1YWM4YjgiLCJ0ZW5hbnRfaWQiOiJ0b2Jfb25saW5lIiwidG9iX3RlbmFudF9pZCI6IjMwNzU3OTM5MiIsInR5cGUiOiJ1c2VyIiwidXNlcl9pZCI6IjMwODc4MDI4OCJ9LCJleHAiOjE3Nzg4MzcxNjQsImlhdCI6MTc3NzYyNzU2NH0.oEiK2iSk0e9GXH27zyaD2s6gI7rBjc5Gt9f5Vy3r7Ag866YTL9itI3kmC-sP9YaUh1R8F3xkSGzWX-THU9hbgeJ2RjfqkMimr5pnK5Hp8v0xZgPaSLkSRMhtvG19_d-8I0HWkJZAJXIE-1d3jb28dqZnvVABmXQW7IWWnk0rYeJvDFjWYpeD25mX1Gu-BR9T4Sn5yGURFhxjoQT-E2WzqeAiISMr0vnUy8jnJCjceQtwCZy_h6OYZw8MMJW50oHvFtb13wYwuUu0j5ByRlbfgYsIg-bFUvYJWZe6sA7Z500mgGnYAsWYk3zczhurD-wEK0Gubt5R49xo87RabgSg53y06F5ec6mC0g8oDTEKip1Tiop5G6UvlrWqUv_cuA9Iks0jcOfggsejh2sKWbWdiCLLoQ4KHH_MJ0eH59kMe_BW6EAJSRDz3bS70RqlXtqCsdY98viwjAcG10NzTqLgL2Z-0v2-0KL-wgYXiWvoUISTX_MWpHArB9BVVeXrncaHYT712zGHhZmk_m6st4lZ9wbH-CAf-tbWcO6Ulw8sqA8QIi-eJbZh2049Xnw0NyVKb4tlXUGC2526KhI_yuhNyHIEqPLAfRgk07osYZWHH0VojTCA6l7ig3QhqhKbdKsPEM7oa6WMpLx2RLT3ChknFsMVy4HSfyeitBYWX4eF5o8"

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
