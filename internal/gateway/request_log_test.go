package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestLogIncludesBothBodiesAndRedactedHeaders(t *testing.T) {
	directory := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_log","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer upstream.Close()
	h, err := New(Config{BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Model: "upstream-model", RequestLogDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(`{"model":"client-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	request.Header.Set("Cookie", "private-cookie")
	request.Header.Set("X-Debug", "keep")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected four files, got %d", len(entries))
	}
	stages := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "client-secret") || strings.Contains(string(raw), "upstream-secret") || strings.Contains(string(raw), "private-cookie") {
			t.Fatal("credential leaked")
		}
		var record struct {
			RequestID    string              `json:"request_id"`
			Stage        string              `json:"stage"`
			Headers      http.Header         `json:"headers"`
			Body         map[string]any      `json:"body"`
			Query        map[string][]string `json:"query"`
			RawBody      string              `json:"raw_body"`
			HeaderSource string              `json:"header_source"`
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		if record.RequestID != w.Header().Get("request-id") {
			t.Fatal("request IDs differ")
		}
		stages[record.Stage] = true
		if record.Headers.Get("Authorization") != "[REDACTED]" {
			t.Fatal("missing redacted auth")
		}
		if record.Stage == "inbound" {
			if record.Body["model"] != "client-model" || record.Headers.Get("X-Debug") != "keep" || record.Query["beta"][0] != "true" {
				t.Fatal(record)
			}
		} else {
			if record.Body["model"] != "upstream-model" || record.Headers.Get("Content-Type") != "application/json" {
				t.Fatal(record)
			}
			if record.Headers.Get("Content-Length") == "" || record.Headers.Get("Accept-Encoding") != "gzip" || record.HeaderSource != "transport" {
				t.Fatalf("actual transport headers missing: %+v", record)
			}
		}
		if record.RawBody == "" {
			t.Fatal("raw request body missing")
		}
		httpRaw, err := os.ReadFile(filepath.Join(directory, strings.TrimSuffix(entry.Name(), ".json")+".http"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(httpRaw), record.RawBody) || !strings.Contains(string(httpRaw), "Authorization: [REDACTED]") {
			t.Fatal("raw HTTP request incomplete")
		}
	}
	if !stages["inbound"] || !stages["upstream"] {
		t.Fatal(stages)
	}
}

func TestRequestLogCapturesRejectedRequestVerbatim(t *testing.T) {
	directory := t.TempDir()
	h, err := New(Config{BaseURL: "http://unused.invalid/v1", APIKey: "secret", GatewayKey: "expected", RequestLogDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	body := "{\n  \"model\" : \"original\", \"messages\" : []\n}"
	r := httptest.NewRequest("POST", "/v1/messages?token=hidden&beta=true", strings.NewReader(body))
	r.Header.Set("X-Debug", "visible")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	files, _ := filepath.Glob(filepath.Join(directory, "*_inbound.json"))
	if len(files) != 1 {
		t.Fatalf("missing rejected request log: %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	var v struct {
		RawBody string `json:"raw_body"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.RawBody != body || strings.Contains(string(raw), "hidden") {
		t.Fatal("raw body missing or query credentials leaked")
	}
}

func TestRequestLogSkipsModelsAndTokenCount(t *testing.T) {
	directory := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"input_tokens":1}`)
		}
	}))
	defer upstream.Close()
	h, err := New(Config{BaseURL: upstream.URL + "/v1", APIKey: "secret", RequestLogDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/models", "/v1/messages/count_tokens"} {
		method := "GET"
		if path != "/v1/models" {
			method = "POST"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"test"}]}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatalf("non-inference requests logged: %v", entries)
	}
}
