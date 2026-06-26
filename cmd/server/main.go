package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"traecli-api-proxy/config"
	"traecli-api-proxy/handler"
	"traecli-api-proxy/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDMiddleware is a Gin middleware that extracts or generates a request ID,
// sets it in the Gin context and response headers, and injects it into the standard
// request context for use in downstream operations and logging.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Set("requestID", requestID)
		c.Header("X-Request-ID", requestID)

		ctx := logger.WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	if err := config.Load(*configPath); err != nil {
		logger.Error(context.Background(), "Failed to load config", "error", err)
		os.Exit(1)
	}

	r := gin.Default()
	r.Use(RequestIDMiddleware())
	// Group for OpenAI-style endpoints
	openaiGroup := r.Group("/v1")
	openaiGroup.Use(handler.AuthMiddleware(false))
	{
		openaiGroup.POST("/chat/completions", handler.HandleChatCompletions)
		openaiGroup.GET("/models", handler.HandleModels)
	}

	// Group for Claude-style endpoints
	claudeGroup := r.Group("/v1")
	claudeGroup.Use(handler.AuthMiddleware(true))
	{
		claudeGroup.POST("/messages", handler.HandleClaudeMessages)
	}

	port := config.Cfg.Server.Port
	if port == 0 {
		port = 8318
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: r,
	}

	go func() {
		logger.Info(context.Background(), fmt.Sprintf("Server running on port %d", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(context.Background(), "Listen error", "error", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with
	// a timeout of 5 seconds.
	quit := make(chan os.Signal, 1)
	// kill (no parameter) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT
	// kill -9 is syscall.SIGKILL but can't be caught, so no need add it
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info(context.Background(), "Shutting down server...")

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error(context.Background(), "Server forced to shutdown", "error", err)
	}

	logger.Info(context.Background(), "Server exiting")
}
