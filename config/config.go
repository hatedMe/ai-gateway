package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Trae   TraeConfig   `yaml:"trae"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type TraeConfig struct {
	Headers map[string]string `yaml:"headers"`
}

type ModelConfig struct {
	ConfigName string
	ModelName  string
}

var Cfg *Config

const TargetURL = "https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat"

var ModelMap = map[string]ModelConfig{
	"Doubao-Seed-2.0-Code": {ConfigName: "Doubao-Seed-2.0-Code", ModelName: "Doubao-Seed-2.0-Code__v2"},
	"Doubao_1_6":           {ConfigName: "Doubao_1_6", ModelName: "Doubao_1_6__v2"},
	"glm-5.1":              {ConfigName: "glm-5.1", ModelName: "glm-5__v2"},
	"glm-5":                {ConfigName: "glm-5", ModelName: "glm-5__v2"},
	"glm-4.7":              {ConfigName: "glm-4.7", ModelName: "glm-4.7__v2"},
	"minimax-m2.7":         {ConfigName: "minimax-m2.7", ModelName: "minimax-m2.7__v2"},
	"minimax-m2.5":         {ConfigName: "minimax-m2.5", ModelName: "minimax-m2.5__v2"},
	"qwen3-coder":          {ConfigName: "qwen3-coder", ModelName: "qwen3-coder__v2"},
	"kimi-k2.6":            {ConfigName: "kimi-k2.6", ModelName: "kimi-k2.6__v2"},
	"kimi-k2.5":            {ConfigName: "kimi-k2.5", ModelName: "kimi-k2.5__v2"},
	"deepseek-V3.2":        {ConfigName: "deepseek-V3.2", ModelName: "deepseek-V3.2__v2"},
	"deepseek-V3.1":        {ConfigName: "deepseek-V3.1", ModelName: "deepseek-V3.1__v2"},
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
