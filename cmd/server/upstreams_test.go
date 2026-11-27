package main

import (
	"ai-gateway-go/internal/config"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredMultiUpstreams(t *testing.T) {
	setupEnv(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" || (body["model"] != "real-code" && body["model"] != "real-fast") {
			t.Error(r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chat_1","model":"real","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	path := saveConfig(t, fmt.Sprintf(`upstreams:
  service:
    protocol: openai-compatible
    base_url: %q
    api_key_env: OPENAI_API_KEY
    models:
      - name: code
        model: real-code
      - name: fast
        model: real-fast
logging:
  requests:
    enabled: true
    dir: requests
`, upstream.URL+"/v1"))
	if err := run([]string{"--config", path, "--check-config"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("check-config accessed upstream")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "requests")); !os.IsNotExist(err) {
		t.Fatal("check-config created logs")
	}
	c, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newServer(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"code", "fast"} {
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hi"}`)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	// Invalid model entries are rejected in check-only mode as well.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := saveConfig(t, strings.ReplaceAll(strings.ReplaceAll(string(raw), "name: fast", "name: code"), "model: real-fast", "model: real-code"))
	if err := run([]string{"--config", invalid, "--check-config"}); err == nil {
		t.Fatal("accepted duplicate public model")
	}
	if calls != 2 {
		t.Fatal("invalid configuration accessed upstream")
	}
}
