package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamProxy(t *testing.T) {
	var target, auth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.URL.String()
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_proxy","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"proxied"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer proxy.Close()
	h, err := New(Config{BaseURL: "http://upstream.invalid/v1", APIKey: "test-key", ProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"test","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "proxied") || target != "http://upstream.invalid/v1/responses" || auth != "Bearer test-key" {
		t.Fatalf("status=%d target=%s body=%s", w.Code, target, w.Body.String())
	}
}

func TestRejectInvalidProxy(t *testing.T) {
	for _, value := range []string{"localhost:7890", "ftp://localhost:7890", "http://", "http://localhost:7890/path", "http://localhost:7890?x=1"} {
		if _, err := New(Config{BaseURL: "https://upstream.invalid/v1", APIKey: "test-key", ProxyURL: value}); err == nil {
			t.Errorf("accepted proxy %s", value)
		}
	}
}
