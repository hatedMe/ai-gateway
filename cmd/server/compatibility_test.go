package main

import (
	"ai-gateway-go/internal/config"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredModelCompatibility(t *testing.T) {
	setupEnv(t)
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","model":"real","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	body := fmt.Sprintf(`upstreams:
  a:
    protocol: openai-compatible
    base_url: %q
    api_key_env: OPENAI_API_KEY
    models:
      - name: old
        model: real
        compatibility:
          chat_token_limit_field: max_tokens
      - name: new
        model: real
        compatibility:
          chat_token_limit_field: max_completion_tokens
  b:
    protocol: openai-compatible
    base_url: %q
    api_key_env: OPENAI_API_KEY
    models:
      - name: other
        model: real
`, upstream.URL, upstream.URL)
	c, err := config.Load(saveConfig(t, body), true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newServer(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, path, fields, want, absent string }{
		{"old", "/v1/responses", `"input":"hi","max_output_tokens":23`, "max_tokens", "max_completion_tokens"},
		{"new", "/v1/responses", `"input":"hi","max_output_tokens":23`, "max_completion_tokens", "max_tokens"},
		{"old", "/v1/chat/completions", `"messages":[],"max_tokens":11,"max_completion_tokens":23,"extension":true`, "max_tokens", "max_completion_tokens"},
		{"other", "/v1/chat/completions", `"messages":[],"max_tokens":23,"extension":true`, "max_tokens", "max_completion_tokens"},
	} {
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"`+tc.model+`",`+tc.fields+`}`)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if got[tc.want] != float64(23) || got[tc.absent] != nil {
			t.Fatalf("%s: %v", tc.model, got)
		}
		if tc.path == "/v1/chat/completions" && got["extension"] != true {
			t.Fatal("lost native extension")
		}
	}
}
