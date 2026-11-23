package gateway

import (
	"bytes"
	"encoding/json"
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

func TestConversionErrorKeepsRequestIDAndOutcome(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	h, _ := New(Config{BaseURL: "http://unused.invalid", APIKey: "test"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"unmapped":true}`)))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var id string
	_ = json.Unmarshal(body["request_id"], &id)
	if w.Code != 400 || id != w.Header().Get("request-id") || !strings.Contains(logs.String(), `"outcome":"conversion_error"`) {
		t.Fatalf("%d %s %s", w.Code, w.Body.String(), logs.String())
	}
}

func TestStreamConversionLogIdentifiesRejectedField(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	h := compatibilityHandler(t, "openai-compatible", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat_test\",\"choices\":[{\"index\":0,\"delta\":{\"provider_metadata\":{\"private\":\"do-not-log-payload\"}},\"finish_reason\":null}]}\n\n")
	})
	w := compatibilitySend(t, h, "openai-responses", compatibilityRequest("openai-responses", true, []string{"lookup"}))
	if !strings.Contains(w.Body.String(), `"field":"provider_metadata"`) || strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("rejected field diagnostics lost: %s", w.Body.String())
	}
	if !strings.Contains(logs.String(), `conversion_field="provider_metadata"`) || strings.Contains(logs.String(), "do-not-log-payload") {
		t.Fatalf("log must identify field without its value: %s", logs.String())
	}
}
