// Package config loads and validates startup configuration without side effects.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

type Config struct {
	Upstreams map[string]UpstreamConfig `yaml:"upstreams"`
	Server    struct {
		MaxConcurrentRequests int           `yaml:"max_concurrent_requests"`
		Addr                  string        `yaml:"addr"`
		ReadBodyTimeout       time.Duration `yaml:"read_body_timeout"`
		WriteIdleTimeout      time.Duration `yaml:"write_idle_timeout"`
		IdleTimeout           time.Duration `yaml:"idle_timeout"`
		ReadHeaderTimeout     time.Duration `yaml:"read_header_timeout"`
		ShutdownTimeout       time.Duration `yaml:"shutdown_timeout"`
	} `yaml:"server"`
	Upstream struct {
		ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
		ReadIdleTimeout       time.Duration `yaml:"read_idle_timeout"`
		BaseURL               string        `yaml:"base_url"`
		APIKey                string        `yaml:"api_key"`
		Model                 string        `yaml:"model"`
		ProxyURL              string        `yaml:"proxy_url"`
	} `yaml:"upstream"`
	Auth struct {
		GatewayAPIKey string `yaml:"gateway_api_key"`
	} `yaml:"auth"`
	Models struct {
		Aliases               map[string]string   `yaml:"aliases"`
		ReasoningEfforts      []string            `yaml:"reasoning_efforts"`
		ModelReasoningEfforts map[string][]string `yaml:"model_reasoning_efforts"`
	} `yaml:"models"`
	Logging struct {
		Requests struct {
			MaxTotalBytes int64         `yaml:"max_total_bytes"`
			Retention     time.Duration `yaml:"retention"`
			Enabled       bool          `yaml:"enabled"`
			Dir           string        `yaml:"dir"`
		} `yaml:"requests"`
	} `yaml:"logging"`
}

// Load applies defaults, a YAML file and environment overrides, in that order.
// Only a missing implicit file is optional. Relative log paths use the file's
// directory when loaded, otherwise the current working directory.
func Load(path string, explicit bool) (Config, error) {
	var c Config
	c.Server.MaxConcurrentRequests = 64
	c.Server.Addr = "127.0.0.1:8176"
	c.Server.ReadHeaderTimeout = 10 * time.Second
	c.Server.ShutdownTimeout = 5 * time.Second
	c.Server.ReadBodyTimeout = 30 * time.Second
	c.Server.WriteIdleTimeout = 60 * time.Second
	c.Server.IdleTimeout = 120 * time.Second
	c.Upstream.ResponseHeaderTimeout = 60 * time.Second
	c.Upstream.ReadIdleTimeout = 120 * time.Second
	c.Upstream.BaseURL = "https://opencode.ai/zen/go/v1"
	c.Upstream.Model = "muse-spark-1.3-contributor"
	c.Upstream.ProxyURL = "http://127.0.0.1:7890"
	c.Logging.Requests.Enabled = false
	c.Logging.Requests.MaxTotalBytes = 1 << 30
	c.Logging.Requests.Retention = 7 * 24 * time.Hour
	c.Logging.Requests.Dir = "logs/requests"

	base := "."
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := decodeYAML(raw, &c); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
		base = filepath.Dir(path)
	} else if explicit || !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("config %s: cannot read file", path)
	}
	if err := c.applyEnv(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	for name, u := range c.Upstreams {
		if u.ResponseHeaderTimeout == 0 {
			u.ResponseHeaderTimeout = 60 * time.Second
		}
		if u.ReadIdleTimeout == 0 {
			u.ReadIdleTimeout = 120 * time.Second
		}
		c.Upstreams[name] = u
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if c.Logging.Requests.Dir != "" {
		dir := c.Logging.Requests.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		c.Logging.Requests.Dir, err = filepath.Abs(dir)
		if err != nil {
			return Config{}, fmt.Errorf("config %s: logging.requests.dir: cannot resolve path", path)
		}
	}
	return c, nil
}

var syntaxLine = regexp.MustCompile(`line ([0-9]+)`)

func decodeYAML(raw []byte, c *Config) error {
	d := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := d.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		// Parser diagnostics may contain the original source, including secrets.
		if line := syntaxLine.FindStringSubmatch(err.Error()); len(line) == 2 {
			return fmt.Errorf("line %s: invalid YAML syntax", line[1])
		}
		return fmt.Errorf("invalid YAML syntax")
	}
	var extra yaml.Node
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("only one YAML document is allowed")
	}
	if len(doc.Content) != 1 {
		return fmt.Errorf("expected a YAML mapping")
	}
	root := doc.Content[0]
	if err := checkNode(root, reflect.TypeOf(*c), "config"); err != nil {
		return err
	}
	var modern, legacy bool
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		modern = modern || key.Value == "upstreams"
		if key.Value == "upstreams" {
			for j := 1; j < len(value.Content); j += 2 {
				entry := value.Content[j]
				hasKey, hasEnv := false, false
				for k := 0; k < len(entry.Content); k += 2 {
					hasKey = hasKey || entry.Content[k].Value == "api_key"
					hasEnv = hasEnv || entry.Content[k].Value == "api_key_env"
				}

				for k := 0; k < len(entry.Content); k += 2 {
					if entry.Content[k].Value != "models" {
						continue
					}
					for _, model := range entry.Content[k+1].Content {
						hasModel, hasAlias := false, false
						for j := 0; j < len(model.Content); j += 2 {
							key, value := model.Content[j], model.Content[j+1]
							if key.Value == "model" {
								hasModel = true
								if strings.TrimSpace(value.Value) == "" {
									return fmt.Errorf("models.model: must not be empty")
								}
							}
							if key.Value == "alias" {
								hasAlias = true
								if strings.TrimSpace(value.Value) == "" {
									return fmt.Errorf("models.alias: must not be empty")
								}
							}
						}
						if hasModel && hasAlias {
							return fmt.Errorf("models: model and alias are mutually exclusive")
						}
					}
				}
				if hasKey && hasEnv {
					return fmt.Errorf("upstreams[entry]: api_key and api_key_env are mutually exclusive")
				}
			}
		}
		legacy = legacy || key.Value == "upstream"
		if key.Value == "models" {
			for j := 0; j < len(value.Content); j += 2 {
				legacy = legacy || value.Content[j].Value == "aliases"
			}
		}
	}
	if modern && legacy {
		return fmt.Errorf("upstreams: cannot combine with legacy upstream or models.aliases")
	}
	if err := root.Decode(c); err != nil {
		return fmt.Errorf("invalid YAML configuration")
	}
	return nil
}

// Validate nodes before decoding: the YAML decoder otherwise coerces numbers
// to strings and ignores nulls. Errors name locations without printing values.
func checkNode(n *yaml.Node, typ reflect.Type, path string) error {
	fail := func(reason string) error { return fmt.Errorf("%s (line %d): %s", path, n.Line, reason) }
	if n.Kind == yaml.AliasNode {
		return fail("YAML aliases are not supported")
	}
	if n.Tag == "!!null" {
		return fail("null is not allowed")
	}
	if typ == reflect.TypeFor[time.Duration]() {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return fail("expected a duration string")
		}
		if value, err := time.ParseDuration(n.Value); err != nil || value <= 0 {
			return fail("expected a positive duration")
		}
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		return checkNode(n, typ.Elem(), path)
	}
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		if n.Kind != yaml.MappingNode {
			return fail("expected a mapping")
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Tag != "!!str" {
				return fail("expected string keys")
			}
			// Unknown and dynamic keys may contain a mistyped secret value.
			fieldPath := path + "[entry]"
			if seen[key.Value] {
				return fmt.Errorf("%s (line %d): duplicate key", path, key.Line)
			}
			seen[key.Value] = true
			var child reflect.Type
			if typ.Kind() == reflect.Map {
				child = typ.Elem()
			} else {
				for j := 0; j < typ.NumField(); j++ {
					f := typ.Field(j)
					if f.Tag.Get("yaml") == key.Value {
						child = f.Type
						fieldPath = path + "." + f.Tag.Get("yaml")
						break
					}
				}
			}
			if child == nil {
				return fmt.Errorf("%s (line %d): unknown field", path, key.Line)
			}
			if err := checkNode(value, child, fieldPath); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return fail("expected a list")
		}
		for i, child := range n.Content {
			if err := checkNode(child, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.String:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return fail("expected a string")
		}
	case reflect.Int, reflect.Int64:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return fail("expected an integer")
		}
	case reflect.Bool:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			return fail("expected a boolean")
		}
	}
	return nil
}

func (c *Config) applyEnv() error {
	if c.Upstreams != nil {
		if err := c.resolveUpstreamKeys(); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"GATEWAY_ADDR", &c.Server.Addr}, {"OPENAI_BASE_URL", &c.Upstream.BaseURL},
		{"OPENAI_API_KEY", &c.Upstream.APIKey}, {"OPENAI_MODEL", &c.Upstream.Model},
		{"OPENAI_PROXY_URL", &c.Upstream.ProxyURL}, {"GATEWAY_API_KEY", &c.Auth.GatewayAPIKey},
	} {
		if c.Upstreams != nil && strings.HasPrefix(field.name, "OPENAI_") {
			continue
		}
		if value, ok := os.LookupEnv(field.name); ok {
			*field.target = value
		}
	}
	if raw, ok := os.LookupEnv("OPENAI_MODEL_ALIASES"); ok && c.Upstreams == nil {
		c.Models.Aliases = nil
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &c.Models.Aliases); err != nil || c.Models.Aliases == nil {
				return fmt.Errorf("OPENAI_MODEL_ALIASES: expected a JSON object of strings")
			}
		}
	}
	if raw, ok := os.LookupEnv("OPENAI_MODEL_REASONING_EFFORTS"); ok {
		c.Models.ModelReasoningEfforts = nil
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &c.Models.ModelReasoningEfforts); err != nil || c.Models.ModelReasoningEfforts == nil {
				return fmt.Errorf("OPENAI_MODEL_REASONING_EFFORTS: expected a JSON object of lists")
			}
			for _, efforts := range c.Models.ModelReasoningEfforts {
				if efforts == nil {
					return fmt.Errorf("OPENAI_MODEL_REASONING_EFFORTS: null is not allowed")
				}
			}
		}
	}
	if raw, ok := os.LookupEnv("OPENAI_REASONING_EFFORTS"); ok {
		c.Models.ReasoningEfforts = nil
		for _, effort := range strings.Split(raw, ",") {
			if effort = strings.TrimSpace(effort); effort != "" {
				c.Models.ReasoningEfforts = append(c.Models.ReasoningEfforts, effort)
			}
		}
	}
	return nil
}

func (c Config) Validate() error {
	if c.Server.MaxConcurrentRequests <= 0 {
		return fmt.Errorf("server.max_concurrent_requests: must be positive")
	}
	_, port, err := net.SplitHostPort(c.Server.Addr)
	if err != nil {
		return fmt.Errorf("server.addr: expected host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("server.addr: port must be between 0 and 65535")
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("server.read_header_timeout: must be positive")
	}
	if c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server.shutdown_timeout: must be positive")
	}
	if c.Upstreams != nil {
		if err := c.validateUpstreams(); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(c.Upstream.APIKey) == "" {
			return fmt.Errorf("upstream.api_key (OPENAI_API_KEY): is required")
		}
		u, err := url.Parse(c.Upstream.BaseURL)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("upstream.base_url: expected an absolute HTTP(S) URL without credentials, query or fragment")
		}
		if c.Upstream.ProxyURL != "" {
			u, err := url.Parse(c.Upstream.ProxyURL)
			if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("upstream.proxy_url: expected an HTTP(S) or SOCKS5 proxy URL without path, query or fragment")
			}
		}
	}
	if c.Logging.Requests.Enabled && (c.Logging.Requests.MaxTotalBytes <= 0 || c.Logging.Requests.Retention <= 0) {
		return fmt.Errorf("logging.requests: capacity and retention must be positive")
	}
	if c.Logging.Requests.Enabled && strings.TrimSpace(c.Logging.Requests.Dir) == "" {
		return fmt.Errorf("logging.requests.dir: is required when logging is enabled")
	}
	for alias, target := range c.Models.Aliases {
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(target) == "" {
			return fmt.Errorf("models.aliases: names and targets must not be empty")
		}
	}
	if err := validateEfforts(c.Models.ReasoningEfforts); err != nil {
		return fmt.Errorf("models.reasoning_efforts: %w", err)
	}
	for model, efforts := range c.Models.ModelReasoningEfforts {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("models.model_reasoning_efforts: model names must not be empty")
		}
		if err := validateEfforts(efforts); err != nil {
			return fmt.Errorf("models.model_reasoning_efforts: %w", err)
		}
	}
	return nil
}

func validateEfforts(efforts []string) error {
	for _, effort := range efforts {
		switch effort {
		case "none", "minimal", "low", "medium", "high", "xhigh":
		default:
			return fmt.Errorf("expected none, minimal, low, medium, high or xhigh")
		}
	}
	return nil
}
