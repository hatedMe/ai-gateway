package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestChatHTTPJSONAndSSE(t *testing.T) {
	final := `{"id":"resp_chat","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello STOP hidden"}]}],"usage":{"input_tokens":10,"output_tokens":4}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			directory := t.TempDir()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Error("wrong upstream path", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "upstream-model" || body["input"] == nil || body["messages"] != nil {
					t.Error("request not converted", body)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", final)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, final)
				}
			}))
			defer upstream.Close()
			h, err := New(Config{BaseURL: upstream.URL + "/v1", APIKey: "up-key", Model: "upstream-model", RequestLogDir: directory})
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"model":"client-model","stream":%t,"stream_options":{"include_usage":true},"stop":"STOP","messages":[{"role":"user","content":"hi"}]}`, stream)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(raw)))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			text := w.Body.String()
			if strings.Contains(text, "hidden") || strings.Contains(text, "event:") {
				t.Fatal("wrong Chat output", text)
			}
			if stream {
				if !strings.Contains(text, "data: [DONE]") || !strings.Contains(text, `"object":"chat.completion.chunk"`) || !strings.Contains(text, `"choices":[]`) {
					t.Fatal(text)
				}
			} else {
				if !strings.Contains(text, `"object":"chat.completion"`) || !strings.Contains(text, `"content":"hello "`) || !strings.Contains(text, `"model":"client-model"`) {
					t.Fatal(text)
				}
			}
			logs, _ := os.ReadDir(directory)
			if len(logs) != 4 {
				t.Fatal("Chat request logs missing", logs)
			}
		})
	}
}

func TestChatHTTPRejectsUnsupportedAndPropagatesFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"quota"}}`)
	}))
	defer upstream.Close()
	h, _ := New(Config{BaseURL: upstream.URL, APIKey: "key"})
	for _, test := range []struct {
		body   string
		status int
	}{{`{"model":"m","n":2,"messages":[{"role":"user","content":"hi"}]}`, 400}, {`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, 429}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["error"] == nil {
			t.Fatal("missing OpenAI-compatible error")
		}
		if test.status == 429 && w.Header().Get("Retry-After") != "3" {
			t.Fatal("missing retry header")
		}
	}
}
