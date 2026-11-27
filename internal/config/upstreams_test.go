package config

import (
	"os"
	"strings"
	"testing"
)

const multiConfig = `upstreams:
  service:
    protocol: openai-compatible
    base_url: https://example.invalid/v1
    api_key_env: TEST_UPSTREAM_KEY
    models:
      - name: coding
        model: real-code
      - name: fast
        model: real-fast
`

func TestMultiUpstreamConfig(t *testing.T) {
	cleanEnv(t)
	_ = os.Unsetenv("OPENAI_API_KEY")
	t.Setenv("TEST_UPSTREAM_KEY", "private-value")
	c, err := Load(writeConfig(t, multiConfig), true)
	if err != nil || c.Upstreams["service"].APIKey != "private-value" {
		t.Fatal(c, err)
	}
	// An explicitly referenced variable is a source, not a legacy override.
	t.Setenv("TEST_UPSTREAM_KEY", "private-value")
	body := strings.ReplaceAll(multiConfig, "TEST_UPSTREAM_KEY", "OPENAI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "private-value")
	c, err = Load(writeConfig(t, body), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstreams["service"].APIKey != "private-value" || c.Upstreams["service"].Models[1].Model != "real-fast" {
		t.Fatal("incorrect upstream config")
	}
}

func TestMultiUpstreamConfigValidation(t *testing.T) {
	cases := []string{
		"upstreams: {}\n",
		strings.ReplaceAll(multiConfig, "openai-compatible", "unknown"),
		strings.ReplaceAll(strings.ReplaceAll(multiConfig, "name: fast", "name: coding"), "model: real-fast", "model: real-code"),
		strings.ReplaceAll(multiConfig, "model: real-fast", "model: ''"),
		strings.ReplaceAll(multiConfig, "- name: fast", "- name: 123"),
		strings.ReplaceAll(multiConfig, "models:\n      - name: coding\n        model: real-code\n      - name: fast\n        model: real-fast", "models: {}"),
		multiConfig + "upstream: {}\n",
		multiConfig + "models:\n  aliases: {}\n",
		strings.ReplaceAll(multiConfig, "base_url: https://example.invalid/v1", "base_url: https://example.invalid/v1/chat/completions"),
		strings.ReplaceAll(multiConfig, "openai-compatible", "anthropic"),
		strings.ReplaceAll(multiConfig, "api_key_env: TEST_UPSTREAM_KEY", "api_key: ''\n    api_key_env: TEST_UPSTREAM_KEY"),
		strings.ReplaceAll(multiConfig, "model: real-fast", "model: real-code\n      - name: coding\n        model: real-code"),
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			cleanEnv(t)
			_ = os.Unsetenv("OPENAI_API_KEY")
			t.Setenv("TEST_UPSTREAM_KEY", "private-value")
			_, err := Load(writeConfig(t, body), true)
			if err == nil {
				t.Fatal("accepted invalid config")
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestMultiUpstreamKeyAndLegacyEnvironment(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			cleanEnv(t)
			_ = os.Unsetenv("OPENAI_API_KEY")
			switch scenario {
			case "empty":
				t.Setenv("TEST_UPSTREAM_KEY", "")
			case "legacy":
				t.Setenv("TEST_UPSTREAM_KEY", "private-value")
				t.Setenv("OPENAI_MODEL", "old-model")
			}
			if _, err := Load(writeConfig(t, multiConfig), true); err == nil {
				t.Fatal("accepted invalid environment")
			}
		})
	}
}
