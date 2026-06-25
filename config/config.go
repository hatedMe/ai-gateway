package config

import (
	"fmt"
	"math/rand/v2"
	"os"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Trae   TraeConfig   `yaml:"trae"`
	Auth   AuthConfig   `yaml:"auth"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type TraeConfig struct {
	Headers map[string]string `yaml:"headers"`
}

type AuthConfig struct {
	Tokens  []string `yaml:"tokens"`   // 真实 token 池，随机取一个转发
	AppKeys []string `yaml:"app_keys"` // 允许访问的 appkey 白名单
}

type ModelConfig struct {
	ConfigName string
	ModelName  string
}

var Cfg *Config

const TargetURL = "https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat"

var ModelMap = map[string]ModelConfig{
	"Doubao-Seed-2.1-turbo":    {ConfigName: "Doubao-Seed-2.1-turbo", ModelName: "Doubao-Seed-2.1-turbo__v2"},
	"Doubao-Seed-2.0-Code":     {ConfigName: "Doubao-Seed-2.0-Code", ModelName: "Doubao-Seed-2.0-Code__v2"},
	"Doubao_1_6":               {ConfigName: "Doubao_1_6", ModelName: "Doubao_1_6__v2"},
	"glm-5.2":                  {ConfigName: "glm-5.2", ModelName: "glm-5.2__dev"},
	"glm-5.1":                  {ConfigName: "glm-5.1", ModelName: "glm-5__v2"},
	"glm-5v-turbo":             {ConfigName: "glm-5v-turbo", ModelName: "glm-5__v2"},
	"glm-5":                    {ConfigName: "glm-5", ModelName: "glm-5__v2"},
	"glm-4.7":                  {ConfigName: "glm-4.7", ModelName: "glm-4.7__v2"},
	"minimax-m3":               {ConfigName: "minimax-m3", ModelName: "minimax-m3__v2"},
	"minimax-m2.7":             {ConfigName: "minimax-m2.7", ModelName: "minimax-m2.7__v2"},
	"minimax-m2.5":             {ConfigName: "minimax-m2.5", ModelName: ""},
	"qwen-3.7-plus":            {ConfigName: "qwen-3.7-plus", ModelName: "qwen-3.7-plus__dev"},
	"qwen3-coder":              {ConfigName: "qwen3-coder", ModelName: "qwen3-coder__v2"},
	"kimi-k2.7-code":           {ConfigName: "kimi-k2.7-code", ModelName: "kimi-k2.7-code__dev"},
	"kimi-k2.6":                {ConfigName: "kimi-k2.6", ModelName: "kimi-k2.6__v2"},
	"kimi-k2.5":                {ConfigName: "kimi-k2.5", ModelName: "kimi-k2.5__v2"},
	"kimi-k2":                  {ConfigName: "kimi-k2", ModelName: ""},
	"deepseek-V4-Pro":          {ConfigName: "deepseek-V4-Pro", ModelName: "deepseek-V4-Pro__v2"},
	"DeepSeek-V4-Flash":        {ConfigName: "DeepSeek-V4-Flash", ModelName: "DeepSeek-V4-Flash__v2"},
	"deepseek-V3.2":            {ConfigName: "deepseek-V3.2", ModelName: "deepseek-V3.2__v2"},
	"deepseek-V3.1":            {ConfigName: "deepseek-V3.1", ModelName: ""},
	"summary":                  {ConfigName: "summary", ModelName: "Doubao_1_6"},
	"custom_model_placeholder": {ConfigName: "custom_model_placeholder", ModelName: "custom_model_placeholder__v2"},
}

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	Cfg = &Config{}
	if err := yaml.Unmarshal(data, Cfg); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	return nil
}

func GetModelConfig(name string) ModelConfig {
	if cfg, ok := ModelMap[name]; ok {
		return cfg
	}
	return ModelConfig{ConfigName: name, ModelName: name + "__v2"}
}

// GetRealToken 校验 appkey 并随机返回一个真实 token，appkey 无效返回空字符串
func GetRealToken(appkey string) string {
	if Cfg == nil {
		return ""
	}
	for _, k := range Cfg.Auth.AppKeys {
		if k == appkey {
			if len(Cfg.Auth.Tokens) == 0 {
				return ""
			}
			return Cfg.Auth.Tokens[rand.IntN(len(Cfg.Auth.Tokens))]
		}
	}
	return ""
}
