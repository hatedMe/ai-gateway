package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-gateway-go/internal/config"
)

func setupEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GATEWAY_CONFIG", "GATEWAY_ADDR", "OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_PROXY_URL", "GATEWAY_API_KEY", "OPENAI_MODEL_ALIASES", "OPENAI_REASONING_EFFORTS", "OPENAI_MODEL_REASONING_EFFORTS"} {
		value, set := os.LookupEnv(name)
		t.Cleanup(func() {
			if set {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
		_ = os.Unsetenv(name)
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Chdir(t.TempDir())
}

func saveConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckConfigAndPathSelection(t *testing.T) {
	setupEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := saveConfig(t, fmt.Sprintf("server:\n  addr: %q\nupstream:\n  base_url: http://unreachable.invalid/v1\n", listener.Addr().String()))
	t.Setenv("GATEWAY_CONFIG", "missing.yaml")
	if err := run([]string{"--config", path, "--check-config"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "logs")); !os.IsNotExist(err) {
		t.Fatal("check created logs")
	}
	if err := run([]string{"--check-config"}); err == nil {
		t.Fatal("ignored GATEWAY_CONFIG")
	}
	t.Setenv("GATEWAY_CONFIG", path)
	if err := run([]string{"--check-config"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG", "")
	if err := run([]string{"--check-config"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.yaml", []byte("server:\n  addr: invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--check-config"}); err == nil {
		t.Fatal("ignored default YAML file")
	}
}

func TestInvalidArgumentsAndConfig(t *testing.T) {
	setupEnv(t)
	for _, args := range [][]string{{"--config", ""}, {"--unknown"}, {"extra"}, {"--config"}, {"--check-config", "extra"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	path := saveConfig(t, "upstream:\n  api_key: file-secret\n")
	t.Setenv("OPENAI_API_KEY", "")
	if err := run([]string{"--config", path, "--check-config"}); err == nil || strings.Contains(err.Error(), "file-secret") {
		t.Fatalf("expected safe invalid key error: %v", err)
	}
}

func TestConfiguredServer(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, modelSource := range []string{"file", "env", "alias"} {
			t.Run(fmt.Sprintf("logging=%t/model=%s", enabled, modelSource), func(t *testing.T) {
				setupEnv(t)
				calls := 0
				wantModel := "file-model"
				if modelSource == "env" {
					wantModel = "env-model"
				}
				if modelSource == "alias" {
					wantModel = "alias-model"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer file-upstream-key" || body["model"] != wantModel {
						t.Errorf("incorrect upstream configuration: path=%s model=%v", r.URL.Path, body["model"])
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"resp_test","status":"completed","model":"upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
				}))
				defer upstream.Close()
				_ = os.Unsetenv("OPENAI_API_KEY")
				if modelSource != "file" {
					t.Setenv("OPENAI_MODEL", "env-model")
				}
				aliases := "{}"
				if modelSource == "alias" {
					aliases = "{client: alias-model}"
				}
				path := saveConfig(t, fmt.Sprintf(`server:
  addr: "127.0.0.1:0"
  read_header_timeout: "2s"
  shutdown_timeout: "3s"
upstream:
  base_url: %q
  api_key: file-upstream-key
  model: file-model
  proxy_url: ""
auth:
  gateway_api_key: local-key
models:
  aliases: %s
logging:
  requests:
    enabled: %t
    dir: requests
`, upstream.URL+"/v1", aliases, enabled))
				c, err := config.Load(path, true)
				if err != nil {
					t.Fatal(err)
				}
				server, err := newServer(c)
				if err != nil {
					t.Fatal(err)
				}
				if server.Addr != "127.0.0.1:0" || server.ReadHeaderTimeout != 2*time.Second || server.WriteTimeout != 0 {
					t.Fatal("incorrect HTTP server timeouts")
				}
				for _, endpoint := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
					body := `{"model":"client","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
					if endpoint == "/v1/responses" {
						body = `{"model":"client","input":"hello"}`
					}
					r := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
					r.Header.Set("x-api-key", "local-key")
					w := httptest.NewRecorder()
					server.Handler.ServeHTTP(w, r)
					if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") {
						t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
					}
				}
				if calls != 3 {
					t.Fatalf("upstream calls=%d", calls)
				}
				entries, err := os.ReadDir(c.Logging.Requests.Dir)
				if enabled {
					if err != nil || len(entries) != 12 {
						t.Fatalf("request logs: files=%d err=%v", len(entries), err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("disabled logging created directory: %v", err)
				}
				w := httptest.NewRecorder()
				server.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`)))
				if w.Code != 401 || calls != 3 {
					t.Fatal("local authentication did not apply")
				}
			})
		}
	}
}
