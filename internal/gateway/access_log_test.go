package gateway

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDCorrelation(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "provider-id")
		fmt.Fprint(w, `{"native":true}`)
	}))
	defer up.Close()
	h, _ := New(Config{BaseURL: up.URL, APIKey: "test"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses?token=private-value", strings.NewReader(`{"model":"m","input":"secret-prompt"}`)))
	id := w.Header().Get("request-id")
	if !strings.HasPrefix(id, "req_") || w.Header().Get("x-upstream-request-id") != "provider-id" {
		t.Fatal("IDs not separated", w.Header())
	}
	if strings.Count(logs.String(), "access ") != 1 || !strings.Contains(logs.String(), id) || !strings.Contains(logs.String(), "provider-id") || strings.Contains(logs.String(), "secret-prompt") || strings.Contains(logs.String(), "private-value") {
		t.Fatal("incorrect access summary", logs.String())
	}
}

func TestAccessSummaryReportsInterruptedStream(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n")
	}))
	defer up.Close()
	h, _ := New(Config{BaseURL: up.URL, APIKey: "test"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != 200 || !strings.Contains(logs.String(), `"outcome":"stream_error"`) || strings.Contains(w.Body.String(), "event: message_stop") {
		t.Fatalf("%d %s %s", w.Code, w.Body.String(), logs.String())
	}
}
