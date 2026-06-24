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
	content := "server:\n  port: 3000\nauth:\n  tokens:\n    - \"Cloud-IDE-JWT real-token\"\n  app_keys:\n    - \"test-token\"\ntrae:\n  headers:\n    X-App-Id: \"test-app-id\""
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
	r.Use(func(c *gin.Context) {
		c.Set("requestID", "test-request-id")
		c.Set(AuthKey, "Cloud-IDE-JWT real-token")
		c.Next()
	})
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
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)
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
	req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestHandleChatCompletions_NoAuth(t *testing.T) {
	setupTestConfig(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Skip setting AuthKey for testing NoAuth
	r.Use(func(c *gin.Context) {
		c.Set("requestID", "test-request-id")
		c.Next()
	})
	r.POST("/v1/chat/completions", HandleChatCompletions)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{\"model\":\"glm-5.1\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"))
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
