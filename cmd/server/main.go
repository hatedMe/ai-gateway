package main

import (
	"flag"
	"fmt"
	"log"

	"traecli-api-proxy/config"
	"traecli-api-proxy/handler"

	"github.com/gin-gonic/gin"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	if err := config.Load(*configPath); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	r := gin.Default()
	r.POST("/v1/chat/completions", handler.HandleChatCompletions)
	r.POST("/v1/messages", handler.HandleClaudeMessages)
	r.GET("/v1/models", handler.HandleModels)

	port := config.Cfg.Server.Port
	if port == 0 {
		port = 3000
	}
	fmt.Printf("Server running on port %d\n", port)
	r.Run(fmt.Sprintf(":%d", port))
}
