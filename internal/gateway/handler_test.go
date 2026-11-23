package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func obj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if e := json.Unmarshal(b, &v); e != nil {
		t.Fatalf("%s: %v", b, e)
	}
	return v
}

func makeGateway(t *testing.T, up http.HandlerFunc) (http.Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(up)
	t.Cleanup(server.Close)
	h, e := New(Config{BaseURL: server.URL + "/v1", APIKey: "upstream-secret", Model: "gpt-test", GatewayKey: "client-secret"})
	if e != nil {
		t.Fatal(e)
	}
	return h, server
}

func call(h http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "client-secret")
	r.Header.Set("anthropic-beta", "do-not-forward")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPToolRoundTrip(t *testing.T) {
	var count atomic.Int32
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != "POST" {
			t.Error(r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-beta") != "" {
			t.Error("incorrect forwarded auth/headers")
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		v := obj(t, raw)
		if v["model"] != "gpt-test" || v["store"] != false {
			t.Error(v)
		}
		w.Header().Set("Content-Type", "application/json")
		if count.Add(1) == 1 {
			fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"上海\"}"}],"usage":{"input_tokens":8,"output_tokens":4}}`)
		} else {
			input := v["input"].([]any)
			last := input[len(input)-1].(map[string]any)
			if last["type"] != "function_call_output" || last["call_id"] != "call_1" || last["output"] != "sunny" {
				t.Error(last)
			}
			fmt.Fprint(w, `{"id":"resp_2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"上海晴天"}]}],"usage":{"input_tokens":16,"output_tokens":5}}`)
		}
	})
	first := call(h, `{"model":"claude-alias","max_tokens":64,"tools":[{"name":"weather","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"weather?"}]}`)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	message := obj(t, first.Body.Bytes())
	if message["stop_reason"] != "tool_use" || message["model"] != "claude-alias" {
		t.Fatal(message)
	}
	next := map[string]any{"model": "claude-alias", "max_tokens": 64, "messages": []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": message["content"]},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "sunny"}}},
	}}
	raw, _ := json.Marshal(next)
	second := call(h, string(raw))
	if second.Code != 200 || obj(t, second.Body.Bytes())["stop_reason"] != "end_turn" || !strings.Contains(second.Body.String(), "上海晴天") {
		t.Fatal(second.Code, second.Body.String())
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
}

func TestHTTPStreamAndTruncation(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if obj(t, raw)["stream"] != true {
					t.Error("upstream stream disabled")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.created","response":{"id":"resp_s","status":"in_progress"}}`+"\n\n")
				if !truncated {
					fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`+"\n\n")
				}
			})
			w := call(h, `{"model":"claude-alias","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !w.Flushed {
				t.Fatal(w.Code, w.Header())
			}
			if truncated {
				if !strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "event: message_stop") {
					t.Fatal(w.Body.String())
				}
			} else if !strings.Contains(w.Body.String(), "event: message_stop") || !strings.Contains(w.Body.String(), "hello") {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestHTTPValidationAuthAndErrors(t *testing.T) {
	var count atomic.Int32
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"quota"}}`)
	})
	invalid := call(h, `{"model":"m","max_tokens":64,"thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`)
	if invalid.Code != 400 || obj(t, invalid.Body.Bytes())["type"] != "error" {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
	noAuth := httptest.NewRecorder()
	h.ServeHTTP(noAuth, httptest.NewRequest("POST", "/v1/messages", strings.NewReader("{}")))
	if noAuth.Code != 401 {
		t.Fatal(noAuth.Code)
	}
	if count.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
	limited := call(h, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if limited.Code != 429 || limited.Header().Get("Retry-After") != "3" || obj(t, limited.Body.Bytes())["error"].(map[string]any)["type"] != "rate_limit_error" {
		t.Fatal(limited.Code, limited.Body.String())
	}
}

func TestHTTPMalformedUpstream(t *testing.T) {
	for _, body := range []string{"not-json", `{"id":"r","status":"failed","error":{"message":"bad"}}`} {
		h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		w := call(h, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != 502 || obj(t, w.Body.Bytes())["type"] != "error" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestHTTPStreamFlushesBeforeUpstreamCompletesAndCancels(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`+"\n\n")
		w.(http.Flusher).Flush()
		close(upstreamStarted)
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-release:
		}
	})
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/messages", strings.NewReader(`{"model":"m","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer client-secret")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	<-upstreamStarted
	buf := make([]byte, 256)
	n, e := resp.Body.Read(buf)
	if e != nil || !bytes.Contains(buf[:n], []byte("message_start")) {
		t.Fatal(string(buf[:n]), e)
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request was not canceled")
	}
}

func TestConfigRejectsInvalidURL(t *testing.T) {
	for _, base := range []string{"", "://bad", "file:///tmp/x", "https://host/v1?token=x", "https://user:pass@host/v1"} {
		if _, e := New(Config{BaseURL: base, APIKey: "key"}); e == nil {
			t.Fatal(base)
		}
	}
}

func TestHTTPThinkingToolHistory(t *testing.T) {
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := obj(t, raw)
		if request["reasoning"].(map[string]any)["summary"] != "auto" {
			t.Error(request)
		}
		input := request["input"].([]any)
		w.Header().Set("Content-Type", "application/json")
		if len(input) == 1 {
			fmt.Fprint(w, `{"id":"resp_think","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"need a tool"}],"encrypted_content":"opaque"},{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]}`)
		} else {
			reasoning := input[1].(map[string]any)
			if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_1" || reasoning["encrypted_content"] != "opaque" {
				t.Error(reasoning)
			}
			fmt.Fprint(w, `{"id":"resp_done","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
		}
	})
	first := call(h, `{"model":"m","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	message := obj(t, first.Body.Bytes())
	blocks := message["content"].([]any)
	if blocks[0].(map[string]any)["type"] != "thinking" || blocks[1].(map[string]any)["type"] != "tool_use" {
		t.Fatal(blocks)
	}
	request := map[string]any{"model": "m", "max_tokens": 8192, "thinking": map[string]any{"type": "enabled", "budget_tokens": 4096}, "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": blocks},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "c", "content": "ok"}}},
	}}
	raw, _ := json.Marshal(request)
	next := call(h, string(raw))
	if next.Code != 200 || !strings.Contains(next.Body.String(), "done") {
		t.Fatal(next.Code, next.Body.String())
	}
}

func TestHTTPModelsAndTokenCount(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/input_tokens" {
			t.Error(r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		v := obj(t, raw)
		if v["model"] != "actual" || v["max_output_tokens"] != nil || v["stream"] != nil {
			t.Error(v)
		}
		w.Header().Set("x-request-id", "up-id")
		fmt.Fprint(w, `{"input_tokens":0}`)
	}))
	defer up.Close()
	h, e := New(Config{BaseURL: up.URL + "/v1", APIKey: "key", ModelAliases: map[string]string{"alias": "actual"}})
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"alias"`) || strings.Contains(w.Body.String(), "actual") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != 200 || obj(t, w.Body.Bytes())["input_tokens"] != float64(0) || w.Header().Get("x-upstream-request-id") != "up-id" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}

func TestHTTPNonStreamSSEAndErrorDetails(t *testing.T) {
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "sse-id")
		fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`+"\n\n")
	})
	w := call(h, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") || w.Header().Get("x-upstream-request-id") != "sse-id" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}

func TestHTTPAuxiliaryErrorsAndModels(t *testing.T) {
	for _, status := range []int{404, 405, 501, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("request-id", "rid")
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"quota","code":"limit","param":"model"}}`)
			})
			r := httptest.NewRequest("POST", "/v1/messages/count_tokens?beta=true", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			r.Header.Set("x-api-key", "client-secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 501
			if status == 429 {
				want = 429
			}
			if w.Code != want || w.Header().Get("x-upstream-request-id") != "rid" || w.Header().Get("Retry-After") != "2" {
				t.Fatal(w.Code, w.Body.String(), w.Header())
			}
			v := obj(t, w.Body.Bytes())
			if v["request_id"] != w.Header().Get("request-id") {
				t.Fatal(v)
			}
			if status == 429 && v["error"].(map[string]any)["code"] != "limit" {
				t.Fatal(v)
			}
		})
	}
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != "GET" {
			t.Error(r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-real","created":1}]}`)
	})
	for _, path := range []string{"/v1/models", "/v1/models/gpt-real"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("x-api-key", "client-secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "1970-01-01T00:00:01Z") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 401 || w.Header().Get("request-id") == "" {
		t.Fatal(w.Code, w.Header())
	}
}

func TestHTTPTokenCountRejectsInvalidUpstream(t *testing.T) {
	for _, body := range []string{`{}`, `{"input_tokens":-1}`, `{"input_tokens":1.5}`, `{"input_tokens":"2"}`} {
		t.Run(body, func(t *testing.T) {
			h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			r := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			r.Header.Set("x-api-key", "client-secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 502 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestHTTPModelPagination(t *testing.T) {
	h, e := New(Config{BaseURL: "https://example.invalid/v1", APIKey: "key", ModelAliases: map[string]string{"a": "one", "b": "two", "c": "three"}})
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		query  string
		status int
		id     string
		more   bool
	}{{"?limit=1", 200, "a", true}, {"?limit=1&after_id=a", 200, "b", true}, {"?before_id=c", 200, "a", false}, {"?limit=0", 400, "", false}, {"?after_id=missing", 400, "", false}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models"+tc.query, nil))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			v := obj(t, w.Body.Bytes())
			if v["first_id"] != tc.id || v["has_more"] != tc.more {
				t.Fatal(tc, v)
			}
		}
	}
}

func TestHTTPFailedStreamErrorDetails(t *testing.T) {
	h, _ := makeGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "failed-id")
		fmt.Fprint(w, "data: "+`{"type":"response.failed","response":{"id":"r","status":"failed","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"quota","param":"model"}}}`+"\n\n")
	})
	for _, stream := range []bool{false, true} {
		w := call(h, fmt.Sprintf(`{"model":"m","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream))
		if !strings.Contains(w.Body.String(), `"type":"rate_limit_error"`) || !strings.Contains(w.Body.String(), `"param":"model"`) || !strings.Contains(w.Body.String(), `"request_id":"`+w.Header().Get("request-id")+`"`) {
			t.Fatal(stream, w.Code, w.Body.String())
		}
	}
}
