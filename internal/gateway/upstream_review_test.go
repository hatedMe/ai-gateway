package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMultiUpstreamRejectsUnsupportedChatFields(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: "anthropic", BaseURL: server.URL, APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"seed":1`, `"presence_penalty":1`, `"logprobs":true`, `"store":true`} {
		raw := `{"model":"public","messages":[{"role":"user","content":"hi"}],` + extra + `}`
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(raw)))
		if w.Code != 400 {
			t.Errorf("%s: %d %s", extra, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi","name":"alice"}]}`)))
	if w.Code != 400 {
		t.Error(w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatal("unsupported request reached upstream", calls)
	}
}

func TestMultiUpstreamErrors(t *testing.T) {
	for _, protocol := range []string{"openai-compatible", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(429)
				fmt.Fprint(w, "upstream limited")
			}))
			defer server.Close()
			h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: protocol, BaseURL: server.URL, APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi"}`)))
			var body map[string]any
			if w.Code != 429 || w.Header().Get("Retry-After") != "3" || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] == nil || calls != 1 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(protocol), strings.NewReader(`{"model":"public"}`)))
			if w.Code != 429 || w.Body.String() != "upstream limited" || calls != 2 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestAnthropicUnsupportedCounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer server.Close()
	h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: "anthropic", BaseURL: server.URL, APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
}
