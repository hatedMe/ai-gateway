package config

import (
	"os"
	"strings"
	"testing"
)

func TestModelCompatibilityTokenField(t *testing.T) {
	cleanEnv(t)
	_ = os.Unsetenv("OPENAI_API_KEY")
	t.Setenv("TEST_UPSTREAM_KEY", "private-value")
	for _, field := range []string{"max_tokens", "max_completion_tokens", "unknown", "''", "12", "null"} {
		body := strings.Replace(multiConfig, "model: real-code", "model: real-code\n        compatibility:\n          chat_token_limit_field: "+field, 1)
		_, err := Load(writeConfig(t, body), true)
		valid := field == "max_tokens" || field == "max_completion_tokens"
		if (err == nil) != valid {
			t.Fatalf("field %s: %v", field, err)
		}
		if valid {
			_, err = Load(writeConfig(t, strings.Replace(body, "openai-compatible", "openai-responses", 1)), true)
			if err == nil {
				t.Fatal("accepted Chat-only option on Responses")
			}
		}
	}
}
