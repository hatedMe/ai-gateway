package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"traecli-api-proxy/config"

	"github.com/gin-gonic/gin"
)

func setupTestConfig(t *testing.T) {
	t.Helper()
	content := `
server:
  port: 3000
trae:
  headers:
    X-App-Id: "test-app-id"
`
	tmpFile, err := os.CreateTemp("", "handler-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })
	tmpFile.WriteString(content)
	tmpFile.Close()
	if err := config.Load(tmpFile.Name()); err != nil {
		t.Fatalf("Load config failed: %v", err)
	}
}

func setupRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/models", HandleModels)
	r.POST("/v1/chat/completions", HandleChatCompletions)
	r.POST("/v1/messages", HandleClaudeMessages)
	return r
}

func TestHandleModels(t *testing.T) {
	setupTestConfig(t)
	r := setupRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/models", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["object"] != "list" {
		t.Errorf("expected object list, got %v", resp["object"])
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data to be array, got %T", resp["data"])
	}
	if len(data) != len(config.ModelMap) {
		t.Errorf("expected %d models, got %d", len(config.ModelMap), len(data))
	}
}

func TestHandleChatCompletions_BadRequest(t *testing.T) {
	setupTestConfig(t)
	r := setupRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestHandleChatCompletions_ModelRequired(t *testing.T) {
	setupTestConfig(t)
	r := setupRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestHandleChatCompletions_NoAuth(t *testing.T) {
	setupTestConfig(t)
	r := setupRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.1","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestHandleClaudeMessages_BadRequest(t *testing.T) {
	setupTestConfig(t)
	r := setupRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/messages", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["type"] != "error" {
		t.Errorf("expected type error, got %v", resp["type"])
	}
}
