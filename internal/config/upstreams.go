package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type ReasoningConfig struct {
	Mode    string         `yaml:"mode"`
	Efforts []string       `yaml:"efforts"`
	Budgets map[string]int `yaml:"budgets"`
}
type CompatibilityConfig struct {
	InputModalities     []string         `yaml:"input_modalities"`
	JSONSchema          *bool            `yaml:"json_schema"`
	Reasoning           *ReasoningConfig `yaml:"reasoning"`
	ChatTokenLimitField *string          `yaml:"chat_token_limit_field"`
}

type ModelConfig struct {
	Compatibility *CompatibilityConfig `yaml:"compatibility"`
	Name          string               `yaml:"name"`
	Model         string               `yaml:"model"`
}

type UpstreamConfig struct {
	Protocol         string        `yaml:"protocol"`
	BaseURL          string        `yaml:"base_url"`
	APIKey           string        `yaml:"api_key"`
	APIKeyEnv        string        `yaml:"api_key_env"`
	ProxyURL         string        `yaml:"proxy_url"`
	AnthropicVersion string        `yaml:"anthropic_version"`
	Models           []ModelConfig `yaml:"models"`
}

func (c *Config) resolveUpstreamKeys() error {
	sources := map[string]bool{}
	for name, u := range c.Upstreams {
		if u.APIKeyEnv != "" {
			if u.APIKey != "" {
				return fmt.Errorf("upstreams[entry]: api_key and api_key_env are mutually exclusive")
			}
			sources[u.APIKeyEnv] = true
			value, ok := os.LookupEnv(u.APIKeyEnv)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("upstreams[entry].api_key_env: referenced environment variable must be nonempty")
			}
			u.APIKey = value
			c.Upstreams[name] = u
		}
	}
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_PROXY_URL", "OPENAI_MODEL_ALIASES"} {
		if _, ok := os.LookupEnv(key); ok && !sources[key] {
			return fmt.Errorf("%s: legacy upstream override is not allowed with upstreams", key)
		}
	}
	return nil
}

func (c Config) validateUpstreams() error {
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("upstreams: at least one upstream is required")
	}
	names := map[string]bool{}
	for name, u := range c.Upstreams {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("upstreams: names must not be empty")
		}
		switch u.Protocol {
		case "openai-responses", "openai-compatible", "anthropic":
		default:
			return fmt.Errorf("upstreams[entry].protocol: unsupported protocol")
		}
		parsed, err := url.Parse(u.BaseURL)
		if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("upstreams[entry].base_url: expected an absolute HTTP(S) URL without credentials, query or fragment")
		}
		for _, suffix := range []string{"/responses", "/chat/completions", "/messages"} {
			if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), suffix) {
				return fmt.Errorf("upstreams[entry].base_url: must not include a resource suffix")
			}
		}
		if strings.TrimSpace(u.APIKey) == "" {
			return fmt.Errorf("upstreams[entry].api_key: is required")
		}
		if u.Protocol == "anthropic" && strings.TrimSpace(u.AnthropicVersion) == "" {
			return fmt.Errorf("upstreams[entry].anthropic_version: is required")
		}
		if u.Protocol != "anthropic" && u.AnthropicVersion != "" {
			return fmt.Errorf("upstreams[entry].anthropic_version: only valid for anthropic")
		}
		if u.ProxyURL != "" {
			p, err := url.Parse(u.ProxyURL)
			if err != nil || p == nil || p.Hostname() == "" || (p.Scheme != "http" && p.Scheme != "https" && p.Scheme != "socks5" && p.Scheme != "socks5h") || (p.Path != "" && p.Path != "/") || p.RawQuery != "" || p.Fragment != "" {
				return fmt.Errorf("upstreams[entry].proxy_url: invalid proxy URL")
			}
		}
		if len(u.Models) == 0 {
			return fmt.Errorf("upstreams[entry].models: at least one model is required")
		}
		for _, m := range u.Models {
			if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Model) == "" {
				return fmt.Errorf("upstreams[entry].models: name and model must not be empty")
			}
			if names[m.Name] {
				return fmt.Errorf("upstreams[entry].models: duplicate public model name")
			}
			if m.Compatibility != nil && m.Compatibility.ChatTokenLimitField != nil {
				field := *m.Compatibility.ChatTokenLimitField
				if u.Protocol != "openai-compatible" || (field != "max_tokens" && field != "max_completion_tokens") {
					return fmt.Errorf("upstreams[entry].models.compatibility.chat_token_limit_field: expected a Chat token field on a Chat upstream")
				}
			}
			if m.Compatibility != nil {
				if err := m.Compatibility.validate(u.Protocol); err != nil {
					return err
				}
			}
			names[m.Name] = true
		}
	}
	return nil
}

func (c CompatibilityConfig) validate(protocol string) error {
	fail := func() error {
		return fmt.Errorf("upstreams[entry].models.compatibility: invalid capability declaration")
	}
	if c.InputModalities != nil {
		seen := map[string]bool{}
		for _, m := range c.InputModalities {
			if (m != "text" && m != "image") || seen[m] {
				return fail()
			}
			seen[m] = true
		}
		if !seen["text"] {
			return fail()
		}
	}
	if r := c.Reasoning; r != nil {
		if len(r.Efforts) == 0 || validateEfforts(r.Efforts) != nil {
			return fail()
		}
		if (protocol == "anthropic" && r.Mode != "budget") || (protocol != "anthropic" && r.Mode != "effort") {
			return fail()
		}
		seen := map[string]bool{}
		for _, e := range r.Efforts {
			if seen[e] {
				return fail()
			}
			seen[e] = true
			if r.Mode == "budget" && e != "none" && r.Budgets[e] < 1024 {
				return fail()
			}
		}
		if r.Mode == "effort" && r.Budgets != nil {
			return fail()
		}
		for effort, budget := range r.Budgets {
			if effort == "none" || !seen[effort] || budget < 1024 {
				return fail()
			}
		}
	}
	return nil
}
