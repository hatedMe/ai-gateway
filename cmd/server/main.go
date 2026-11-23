package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"ai-gateway-go/internal/gateway"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://opencode.ai/zen/go/v1"
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = "oc_sk_0c8b0ecc6fea_tVCyHqs5bo3Z1gVPZ5JH-lDQx1XkEzaC"
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "muse-spark-1.3-contributor"
	}
	address := os.Getenv("GATEWAY_ADDR")
	if address == "" {
		address = "127.0.0.1:8176"
	}
	var aliases map[string]string
	var capabilities map[string][]string
	if raw := os.Getenv("OPENAI_MODEL_ALIASES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
			return fmt.Errorf("OPENAI_MODEL_ALIASES: %w", err)
		}
	}
	if raw := os.Getenv("OPENAI_MODEL_REASONING_EFFORTS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
			return fmt.Errorf("OPENAI_MODEL_REASONING_EFFORTS: %w", err)
		}
	}
	var efforts []string
	if raw := os.Getenv("OPENAI_REASONING_EFFORTS"); raw != "" {
		for _, effort := range strings.Split(raw, ",") {
			if effort = strings.TrimSpace(effort); effort != "" {
				efforts = append(efforts, effort)
			}
		}
	}
	proxyURL := os.Getenv("OPENAI_PROXY_URL")
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:7890"
	}
	handler, err := gateway.New(gateway.Config{
		RequestLogDir: "logs/requests",
		ProxyURL:      proxyURL,
		ModelAliases:  aliases, ReasoningEfforts: efforts, ModelReasoningEfforts: capabilities,
		BaseURL: base, APIKey: apiKey, Model: model, GatewayKey: os.Getenv("GATEWAY_API_KEY"),
	})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopped := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		case <-stopped:
		}
	}()
	defer close(stopped)
	log.Printf("Messages gateway listening on %s", address)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}
