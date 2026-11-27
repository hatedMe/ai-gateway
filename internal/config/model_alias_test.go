package config

import (
	"strings"
	"testing"
)

func TestModelAliasConfigFormats(t *testing.T) {
	for _, tc := range []struct{ entry, public, real string }{
		{"name: real\n        alias: coding", "coding", "real"},
		{"name: real", "real", "real"},
		{"name: coding\n        model: real", "coding", "real"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			cleanEnv(t)
			body := "upstreams:\n  svc:\n    protocol: openai-compatible\n    base_url: https://example.invalid/v1\n    api_key_env: OPENAI_API_KEY\n    models:\n      - " + tc.entry + "\n"
			c, err := Load(writeConfig(t, body), true)
			if err != nil {
				t.Fatal(err)
			}
			m, err := c.Upstreams["svc"].Models[0].Resolve()
			if err != nil || m.Name != tc.public || m.Model != tc.real || m.Weight != 1 {
				t.Fatalf("%+v %v", m, err)
			}
		})
	}
}

func TestModelAliasConfigValidation(t *testing.T) {
	for _, entry := range []string{"name: ''", "name: real\n        alias: ''", "name: real\n        model: ''", "name: a\n        model: b\n        alias: ''", "name: a\n        model: b\n        alias: c", "name: a\n        weight: 0", "name: a\n        weight: -1", "name: a\n        weight: 1000001", "name: a\n        weight: '3'", "name: a\n        weight: 1.5", "name: a\n        weight: null"} {
		t.Run(strings.ReplaceAll(entry, "\n", "/"), func(t *testing.T) {
			cleanEnv(t)
			body := "upstreams:\n  svc:\n    protocol: openai-compatible\n    base_url: https://example.invalid/v1\n    api_key_env: OPENAI_API_KEY\n    models:\n      - " + entry + "\n"
			if _, err := Load(writeConfig(t, body), true); err == nil {
				t.Fatal("accepted ambiguous or invalid entry")
			}
		})
	}
}
