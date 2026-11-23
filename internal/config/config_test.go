package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var envNames = []string{"GATEWAY_ADDR", "OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_PROXY_URL", "GATEWAY_API_KEY", "OPENAI_MODEL_ALIASES", "OPENAI_REASONING_EFFORTS", "OPENAI_MODEL_REASONING_EFFORTS"}

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, name := range envNames {
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
	t.Setenv("OPENAI_API_KEY", "test-secret")
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAndMissingFile(t *testing.T) {
	cleanEnv(t)
	path := filepath.Join(t.TempDir(), "missing.yaml")
	c, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Addr != "127.0.0.1:8176" || c.Server.ReadHeaderTimeout != 10*time.Second || c.Server.ShutdownTimeout != 5*time.Second || c.Upstream.APIKey != "test-secret" || c.Upstream.Model != "muse-spark-1.3-contributor" || c.Upstream.BaseURL != "https://opencode.ai/zen/go/v1" || c.Upstream.ProxyURL != "http://127.0.0.1:7890" || !c.Logging.Requests.Enabled {
		t.Fatal("incorrect defaults")
	}
	if !filepath.IsAbs(c.Logging.Requests.Dir) {
		t.Fatal("expected resolved log directory")
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("accepted missing explicit file")
	}
}

func TestYAMLAndOverrides(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `server:
  addr: "localhost:9000"
  read_header_timeout: "2s"
  shutdown_timeout: "3s"
upstream:
  api_key: "file-secret"
  model: ""
  proxy_url: ""
models:
  aliases: {client: file-model}
  reasoning_efforts: [low, high]
  model_reasoning_efforts: {file-model: [high]}
logging:
  requests:
    enabled: false
    dir: "relative/logs"
`)
	c, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream.APIKey != "test-secret" || c.Upstream.Model != "" || c.Upstream.ProxyURL != "" || c.Logging.Requests.Enabled || c.Server.ReadHeaderTimeout != 2*time.Second || c.Server.ShutdownTimeout != 3*time.Second || c.Models.Aliases["client"] != "file-model" || len(c.Models.ReasoningEfforts) != 2 {
		t.Fatal("YAML or environment precedence failed")
	}
	if c.Logging.Requests.Dir != filepath.Join(filepath.Dir(path), "relative/logs") {
		t.Fatal(c.Logging.Requests.Dir)
	}
	t.Setenv("OPENAI_MODEL_ALIASES", `{"other":"env-model"}`)
	t.Setenv("OPENAI_MODEL_REASONING_EFFORTS", `{"env-model":["none","minimal","xhigh"]}`)
	t.Setenv("OPENAI_REASONING_EFFORTS", " low, high, ")
	t.Setenv("GATEWAY_ADDR", ":9001")
	t.Setenv("GATEWAY_API_KEY", "local-secret")
	t.Setenv("OPENAI_BASE_URL", "https://upstream.invalid/v1")
	t.Setenv("OPENAI_MODEL", "env-model")
	t.Setenv("OPENAI_PROXY_URL", "http://proxy.invalid:8080")
	c, err = Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models.Aliases) != 1 || c.Models.Aliases["other"] != "env-model" || len(c.Models.ModelReasoningEfforts) != 1 || c.Upstream.Model != "env-model" || c.Server.Addr != ":9001" || c.Auth.GatewayAPIKey != "local-secret" || c.Upstream.BaseURL != "https://upstream.invalid/v1" || c.Upstream.ProxyURL != "http://proxy.invalid:8080" {
		t.Fatal("environment replacement failed")
	}
	for _, name := range []string{"OPENAI_MODEL", "OPENAI_PROXY_URL", "GATEWAY_API_KEY", "OPENAI_MODEL_ALIASES", "OPENAI_REASONING_EFFORTS", "OPENAI_MODEL_REASONING_EFFORTS"} {
		t.Setenv(name, "")
	}
	c, err = Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream.Model != "" || c.Upstream.ProxyURL != "" || c.Auth.GatewayAPIKey != "" || len(c.Models.Aliases)+len(c.Models.ReasoningEfforts)+len(c.Models.ModelReasoningEfforts) != 0 {
		t.Fatal("empty environment did not clear values")
	}
}

func TestRejectInvalidYAML(t *testing.T) {
	for i, body := range []string{
		"server:\n  typo: secret-value\n", "server:\n  addr: one\n  addr: two\n", "upstream: null\n", "upstream:\n  api_key: null\n",
		"logging:\n  requests:\n    enabled: nope\n", "upstream:\n  model: 123\n", "models:\n  aliases: [bad]\n",
		"models:\n  aliases: {client: 123}\n", "models:\n  reasoning_efforts: [null]\n", "server: []\n",
		"server:\n  read_header_timeout: 12\n", "{}\n---\n{}\n", "upstream: [secret-value\n", "[]\n",
		"upstream: {api_key:secret-value}\n", "models:\n  aliases: {secret-value: 123}\n",
		"models:\n  aliases: {secret-value: one, secret-value: two}\n",
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanEnv(t)
			path := writeConfig(t, body)
			_, err := Load(path, true)
			if err == nil {
				t.Fatal("accepted invalid YAML")
			}
			if !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("unsafe or incomplete error: %s", err)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	for i, body := range []string{
		"server:\n  addr: localhost\n", "server:\n  addr: ':70000'\n", "server:\n  addr: ':abc'\n",
		"server:\n  shutdown_timeout: '0s'\n", "server:\n  read_header_timeout: '-1s'\n", "server:\n  read_header_timeout: 'secret-value'\n",
		"upstream:\n  base_url: ftp://example.com\n", "upstream:\n  base_url: https://user:secret-value@example.com\n",
		"upstream:\n  proxy_url: 'http://user:secret-value@host/path'\n", "logging:\n  requests:\n    dir: ''\n",
		"models:\n  aliases: {client: ''}\n", "models:\n  reasoning_efforts: [unknown]\n", "models:\n  model_reasoning_efforts: {test: [unknown]}\n",
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanEnv(t)
			_, err := Load(writeConfig(t, body), true)
			if err == nil {
				t.Fatal("accepted invalid configuration")
			}
			if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "test-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestInvalidEnvironment(t *testing.T) {
	for i, tc := range []struct{ name, value string }{
		{"OPENAI_API_KEY", ""}, {"OPENAI_API_KEY", "   "}, {"GATEWAY_ADDR", ""}, {"OPENAI_BASE_URL", ""},
		{"OPENAI_MODEL_ALIASES", `{"x":123}`}, {"OPENAI_MODEL_ALIASES", `null`}, {"OPENAI_MODEL_ALIASES", `{"x":""}`},
		{"OPENAI_MODEL_REASONING_EFFORTS", `{"x":null}`}, {"OPENAI_REASONING_EFFORTS", "secret-value"},
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanEnv(t)
			t.Setenv(tc.name, tc.value)
			_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), false)
			if err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("expected safe validation error, got %v", err)
			}
		})
	}
}

func TestLogPaths(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, "logging:\n  requests:\n    dir: logs\n")
	t.Chdir(t.TempDir())
	c, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Logging.Requests.Dir != filepath.Join(filepath.Dir(path), "logs") {
		t.Fatal(c.Logging.Requests.Dir)
	}
	if _, err := os.Stat(c.Logging.Requests.Dir); !os.IsNotExist(err) {
		t.Fatal("loading created log directory")
	}
	abs := filepath.Join(t.TempDir(), "absolute")
	path = writeConfig(t, "logging:\n  requests:\n    dir: '"+abs+"'\n")
	c, err = Load(path, true)
	if err != nil || c.Logging.Requests.Dir != abs {
		t.Fatalf("absolute path failed: %v", err)
	}
}
