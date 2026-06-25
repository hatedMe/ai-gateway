package config

import (
	"os"
	"testing"
)

func createTestConfig(t *testing.T) string {
	t.Helper()
	content := `
server:
  port: 3000
trae:
  headers:
    X-App-Id: "test-app-id"
`
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })
	tmpFile.WriteString(content)
	tmpFile.Close()
	return tmpFile.Name()
}

func TestLoad(t *testing.T) {
	path := createTestConfig(t)
	if err := Load(path); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if Cfg == nil {
		t.Fatal("Cfg should not be nil after Load")
	}
	if Cfg.Server.Port != 3000 {
		t.Errorf("expected port 3000, got %d", Cfg.Server.Port)
	}
}

func TestTargetURL(t *testing.T) {
	if TargetURL != "https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat" {
		t.Errorf("unexpected TargetURL: %s", TargetURL)
	}
}

func TestGetModelConfig_KnownModel(t *testing.T) {
	cfg := GetModelConfig("glm-5.1")
	if cfg.ConfigName != "glm-5.1" {
		t.Errorf("expected ConfigName glm-5.1, got %s", cfg.ConfigName)
	}
	if cfg.ModelName != "glm-5__v2" {
		t.Errorf("expected ModelName glm-5__v2, got %s", cfg.ModelName)
	}
}

func TestGetModelConfig_UnknownModel(t *testing.T) {
	cfg := GetModelConfig("some-unknown-model")
	if cfg.ConfigName != "some-unknown-model" {
		t.Errorf("expected ConfigName some-unknown-model, got %s", cfg.ConfigName)
	}
	if cfg.ModelName != "some-unknown-model__v2" {
		t.Errorf("expected ModelName some-unknown-model__v2, got %s", cfg.ModelName)
	}
}

func TestModelMapCount(t *testing.T) {
	if len(ModelMap) < 20 {
		t.Errorf("expected at least 20 models in ModelMap, got %d", len(ModelMap))
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	err := Load("nonexistent.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoad_Headers(t *testing.T) {
	path := createTestConfig(t)
	Load(path)

	if v, ok := Cfg.Trae.Headers["X-App-Id"]; !ok || v != "test-app-id" {
		t.Errorf("expected X-App-Id test-app-id, got %s", v)
	}
}
