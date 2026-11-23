package main

import (
	"ai-gateway-go/internal/translate"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"

	"ai-gateway-go/internal/config"
	"ai-gateway-go/internal/gateway"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "YAML configuration file")
	check := flags.Bool("check-config", false, "validate configuration without starting the server")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	explicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicit = true
		}
	})
	if explicit && *path == "" {
		return fmt.Errorf("--config: path must not be empty")
	}
	if !explicit {
		if env := os.Getenv("GATEWAY_CONFIG"); env != "" {
			*path, explicit = env, true
		} else {
			*path = "config.yaml"
		}
	}
	c, err := config.Load(*path, explicit)
	if err != nil {
		return err
	}
	if *check {
		log.Printf("Configuration valid (path=%s; environment overrides applied; implicit missing file allowed=%t)", *path, !explicit)
		return nil
	}
	server, err := newServer(c)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopped := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), c.Server.ShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		case <-stopped:
		}
	}()
	defer close(stopped)
	log.Printf("Messages gateway listening on %s", server.Addr)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

func newServer(c config.Config) (*http.Server, error) {
	logDir := ""
	if c.Logging.Requests.Enabled {
		logDir = c.Logging.Requests.Dir
	}
	var upstreams map[string]gateway.Upstream
	if c.Upstreams != nil {
		upstreams = map[string]gateway.Upstream{}
		for name, u := range c.Upstreams {
			models := make([]gateway.Model, 0, len(u.Models))
			for _, m := range u.Models {
				caps := translate.ModelCapabilities{}
				if m.Compatibility != nil && m.Compatibility.ChatTokenLimitField != nil {
					caps.ChatTokenLimitField = *m.Compatibility.ChatTokenLimitField
				}
				if m.Compatibility != nil {
					caps.InputModalities = append([]string(nil), m.Compatibility.InputModalities...)
					if m.Compatibility.JSONSchema != nil {
						caps.JSONSchema = *m.Compatibility.JSONSchema
					}
					if r := m.Compatibility.Reasoning; r != nil {
						caps.Reasoning = &translate.ReasoningCapabilities{Mode: r.Mode, Efforts: append([]string(nil), r.Efforts...), Budgets: map[string]int{}}
						for e, b := range r.Budgets {
							caps.Reasoning.Budgets[e] = b
						}
					}
				}
				models = append(models, gateway.Model{Name: m.Name, Model: m.Model, Capabilities: caps})
			}
			upstreams[name] = gateway.Upstream{Protocol: u.Protocol, BaseURL: u.BaseURL, APIKey: u.APIKey, ProxyURL: u.ProxyURL, AnthropicVersion: u.AnthropicVersion, Models: models, ResponseHeaderTimeout: u.ResponseHeaderTimeout, ReadIdleTimeout: u.ReadIdleTimeout}
		}
	}
	handler, err := gateway.New(gateway.Config{
		Upstreams:       upstreams,
		ReadBodyTimeout: c.Server.ReadBodyTimeout, WriteIdleTimeout: c.Server.WriteIdleTimeout, ResponseHeaderTimeout: c.Upstream.ResponseHeaderTimeout, ReadIdleTimeout: c.Upstream.ReadIdleTimeout,
		RequestLogDir: logDir,
		ProxyURL:      c.Upstream.ProxyURL,
		BaseURL:       c.Upstream.BaseURL, APIKey: c.Upstream.APIKey, Model: c.Upstream.Model,
		GatewayKey:   c.Auth.GatewayAPIKey,
		ModelAliases: c.Models.Aliases, ReasoningEfforts: c.Models.ReasoningEfforts,
		ModelReasoningEfforts: c.Models.ModelReasoningEfforts,
	})
	if err != nil {
		return nil, err
	}
	return &http.Server{Addr: c.Server.Addr, Handler: handler, ReadHeaderTimeout: c.Server.ReadHeaderTimeout, IdleTimeout: c.Server.IdleTimeout}, nil
}
