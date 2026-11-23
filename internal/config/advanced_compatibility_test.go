package config

import (
	"os"
	"strings"
	"testing"
)

func TestAdvancedModelCompatibility(t *testing.T) {
	cleanEnv(t)
	_ = os.Unsetenv("OPENAI_API_KEY")
	t.Setenv("TEST_UPSTREAM_KEY", "key")
	for _, tc := range []struct {
		fields string
		valid  bool
	}{
		{"input_modalities: [text, image]\n          json_schema: true\n          reasoning:\n            mode: effort\n            efforts: [none, low, high]", true},
		{"input_modalities: [image]", false}, {"input_modalities: [text, text]", false}, {"json_schema: 1", false},
		{"reasoning: {mode: effort, efforts: [low, low]}", false}, {"reasoning: {mode: effort, efforts: [unknown]}", false},
		{"reasoning: {mode: budget, efforts: [low], budgets: {low: 1024}}", false},
	} {
		body := strings.Replace(multiConfig, "model: real-code", "model: real-code\n        compatibility:\n          "+tc.fields, 1)
		_, err := Load(writeConfig(t, body), true)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.fields, err)
		}
	}
}
