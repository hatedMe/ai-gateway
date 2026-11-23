package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeResponsesFlushAndCancel(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	h, err := New(Config{BaseURL: upstream.URL + "/v1", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(h)
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", local.URL+"/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal("stream was not flushed", err)
	}
	defer response.Body.Close()
	first := make([]byte, len("event: response.created\n"))
	if _, err = io.ReadFull(response.Body, first); err != nil || string(first) != "event: response.created\n" {
		t.Fatal(string(first), err)
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}
}

func TestNativeResponses(t *testing.T) {
	terminal := `{"id":"resp_native","object":"response","model":"upstream","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"call_a","name":"f","arguments":"{\"id\":9007199254740993}"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6},"future_field":{"id":9007199254740993}}`
	streamBody := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_native\",\"status\":\"in_progress\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":" + terminal + "}\n\n"
	for _, test := range []struct {
		name, path        string
		stream, forcedSSE bool
		status            int
	}{
		{"json", "/v1/responses", false, false, 200},
		{"stream", "/v1/responses", true, false, 200},
		{"aggregate", "/v1/responses", false, true, 200},
		{"error", "/v1/responses", false, false, 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := t.TempDir()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer upstream-key" {
					t.Error("wrong upstream request", r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				var root map[string]json.RawMessage
				if err := json.Unmarshal(body, &root); err != nil {
					t.Error(err)
				}
				if string(root["model"]) != `"upstream"` || string(root["input"]) != `"hello"` || string(root["future_option"]) != `{"id":9007199254740993}` || string(root["reasoning"]) != `{"effort":"xhigh"}` {
					t.Error("native request changed", string(body))
				}
				w.Header().Set("x-request-id", "native-id")
				if test.status != 200 {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "5")
					w.WriteHeader(test.status)
					fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"busy","code":"native_code"},"extra":42}`)
					return
				}
				if test.stream || test.forcedSSE {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, streamBody)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, terminal)
				}
			}))
			defer up.Close()
			h, err := New(Config{BaseURL: up.URL + "/v1", APIKey: "upstream-key", Model: "default", ModelAliases: map[string]string{"client": "upstream"}, GatewayKey: "local", RequestLogDir: logs})
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"model":"client","input":"hello","stream":%t,"reasoning":{"effort":"xhigh"},"future_option":{"id":9007199254740993}}`, test.stream)
			r := httptest.NewRequest("POST", test.path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer local")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status || w.Header().Get("x-upstream-request-id") != "native-id" {
				t.Fatal(w.Code, w.Body.String(), w.Header())
			}
			expected := terminal
			if test.stream {
				expected = streamBody
			}
			if test.status != 200 {
				expected = `{"error":{"type":"rate_limit_error","message":"busy","code":"native_code"},"extra":42}`
				if w.Header().Get("Retry-After") != "5" {
					t.Fatal("missing Retry-After")
				}
			}
			if w.Body.String() != expected {
				t.Fatal("native output changed", w.Body.String())
			}
			files, _ := os.ReadDir(logs)
			if len(files) != 4 {
				t.Fatal("missing inference logs", len(files))
			}
		})
	}
}

func TestNativeResponsesValidation(t *testing.T) {
	h, _ := New(Config{BaseURL: "http://127.0.0.1:1/v1", APIKey: "key", GatewayKey: "local"})
	for _, test := range []struct {
		method, body, key string
		status            int
	}{
		{"GET", "", "local", 405},
		{"POST", `{"model":"m","input":"hi"}`, "wrong", 401},
		{"POST", `[]`, "local", 400},
		{"POST", `{"input":"hi"}`, "local", 400},
		{"POST", `{"model":"m","stream":"yes"}`, "local", 400},
	} {
		r := httptest.NewRequest(test.method, "/v1/responses", strings.NewReader(test.body))
		r.Header.Set("x-api-key", test.key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}

func TestOpenCodeStringInputSession(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	a := openCodeSession(r, []byte(`{"input":"first"}`))
	b := openCodeSession(r, []byte(`{"input":"second"}`))
	if a == b || a == "gateway_auxiliary" {
		t.Fatal("string inputs share fallback session", a, b)
	}
}
