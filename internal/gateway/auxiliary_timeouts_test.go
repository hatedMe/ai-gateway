package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuxiliaryAndUncommittedStreamTimeouts(t *testing.T) {
	for _, scenario := range []string{"models-headers", "models-body", "count-body", "error-body", "messages-stream", "chat-upstream-stream"} {
		t.Run(scenario, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if scenario == "models-headers" {
					<-r.Context().Done()
					return
				}
				if scenario == "messages-stream" || scenario == "chat-upstream-stream" {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				status := 200
				if scenario == "error-body" {
					status = 429
				}
				w.WriteHeader(status)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer up.Close()
			cfg := Config{BaseURL: up.URL, APIKey: "test", ResponseHeaderTimeout: 40 * time.Millisecond, ReadIdleTimeout: 40 * time.Millisecond}
			if scenario == "chat-upstream-stream" {
				cfg.Upstreams = map[string]Upstream{"chat": {Protocol: "openai-compatible", BaseURL: up.URL, APIKey: "test", ResponseHeaderTimeout: 40 * time.Millisecond, ReadIdleTimeout: 40 * time.Millisecond, Models: []Model{{Name: "m", Model: "real"}}}}
			}
			h, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			method, path, body := "POST", "/v1/messages", `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
			if strings.HasPrefix(scenario, "models") {
				method = "GET"
				path = "/v1/models"
				body = ""
			}
			if scenario == "count-body" {
				path = "/v1/messages/count_tokens"
			}
			if strings.Contains(scenario, "stream") {
				body = `{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
			if w.Code != 504 {
				t.Fatalf("want 504 before response committed: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "event: message_stop") {
				t.Fatal("timeout synthesized success")
			}
		})
	}
}
