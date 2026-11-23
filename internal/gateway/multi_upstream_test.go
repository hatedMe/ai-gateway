package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeUpstreamProtocols(t *testing.T) {
	for _, tc := range []struct{ protocol, path string }{{"openai-responses", "/v1/responses"}, {"openai-compatible", "/v1/chat/completions"}, {"anthropic", "/v1/messages"}} {
		t.Run(tc.protocol, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != tc.path {
					t.Error(r.URL.Path)
				}
				if tc.protocol == "anthropic" {
					if r.Header.Get("x-api-key") != "up-secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Error("wrong anthropic auth")
					}
				} else if r.Header.Get("Authorization") != "Bearer up-secret" || r.Header.Get("x-api-key") != "" {
					t.Error("wrong bearer auth")
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != "real-fast" || body["extension"] != true {
					t.Error(body)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"native":true,"model":"real-fast"}`)
			}))
			defer upstream.Close()
			h, err := New(Config{GatewayKey: "local", Upstreams: map[string]Upstream{"svc": {Protocol: tc.protocol, BaseURL: upstream.URL + "/v1", APIKey: "up-secret", AnthropicVersion: func() string {
				if tc.protocol == "anthropic" {
					return "2023-06-01"
				}
				return ""
			}(), Models: []Model{{"fast", "real-fast"}, {"slow", "real-slow"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"fast","extension":true}`))
			r.Header.Set("Authorization", "Bearer local")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"native":true`) {
				t.Fatal(w.Code, w.Body.String())
			}
			r = httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"unknown"}`))
			r.Header.Set("Authorization", "Bearer local")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "model_not_found") || calls != 1 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			r = httptest.NewRequest("GET", "/v1/models", nil)
			r.Header.Set("Authorization", "Bearer local")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"fast"`) || strings.Contains(w.Body.String(), "real-fast") || calls != 1 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func toolFixture(protocol string, stream bool) string {
	calls := []map[string]any{{"id": "call_1", "name": "lookup", "arguments": `{"q":"one"}`}, {"id": "call_2", "name": "lookup", "arguments": `{"q":"two"}`}}
	obj := map[string]any{"id": "resp_tools", "model": "real", "status": "completed", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2}}
	output := []any{}
	for _, c := range calls {
		output = append(output, map[string]any{"type": "function_call", "call_id": c["id"], "name": c["name"], "arguments": c["arguments"]})
	}
	obj["output"] = output
	encoded, _ := json.Marshal(obj)
	if protocol == "openai-responses" {
		if stream {
			return "data: {\"type\":\"response.completed\",\"response\":" + string(encoded) + "}\n\n"
		}
		return string(encoded)
	}
	if !stream {
		if protocol == "openai-compatible" {
			tools := []any{}
			for _, c := range calls {
				tools = append(tools, map[string]any{"id": c["id"], "type": "function", "function": map[string]any{"name": c["name"], "arguments": c["arguments"]}})
			}
			obj = map[string]any{"id": "chat_tools", "model": "real", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": tools}, "finish_reason": "tool_calls"}}}
		} else {
			content := []any{}
			for _, c := range calls {
				var args any
				_ = json.Unmarshal([]byte(c["arguments"].(string)), &args)
				content = append(content, map[string]any{"type": "tool_use", "id": c["id"], "name": c["name"], "input": args})
			}
			obj = map[string]any{"id": "msg_tools", "model": "real", "type": "message", "role": "assistant", "content": content, "stop_reason": "tool_use"}
		}
		raw, _ := json.Marshal(obj)
		return string(raw)
	}
	var result strings.Builder
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(&result, "data: %s\n\n", b) }
	if protocol == "anthropic" {
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_tools", "model": "real", "usage": map[string]any{"input_tokens": 4}}})
	}
	for i, c := range calls {
		args := c["arguments"].(string)
		if protocol == "openai-compatible" {
			emit(map[string]any{"id": "chat_tools", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": c["id"], "type": "function", "function": map[string]any{"name": c["name"], "arguments": args[:4]}}}}, "finish_reason": nil}}})
			emit(map[string]any{"id": "chat_tools", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": args[4:]}}}}, "finish_reason": nil}}})
		} else {
			emit(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": c["id"], "name": c["name"], "input": map[string]any{}}})
			for _, part := range []string{args[:4], args[4:]} {
				emit(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": part}})
			}
			emit(map[string]any{"type": "content_block_stop", "index": i})
		}
	}
	if protocol == "openai-compatible" {
		emit(map[string]any{"id": "chat_tools", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		result.WriteString("data: [DONE]\n\n")
	} else {
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 2}})
		emit(map[string]any{"type": "message_stop"})
	}
	return result.String()
}

func TestMultiUpstreamToolRoundTrip(t *testing.T) {
	for _, target := range []string{"openai-responses", "openai-compatible", "anthropic"} {
		for _, inbound := range []string{"openai-responses", "openai-compatible", "anthropic"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_to_%s/%t", inbound, target, stream), func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						body, _ := io.ReadAll(r.Body)
						if calls > 1 && (!strings.Contains(string(body), "call_1") || !strings.Contains(string(body), "result-two")) {
							t.Error("tool history lost", string(body))
						}
						w.Header().Set("Content-Type", "application/json")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						}
						fmt.Fprint(w, toolFixture(target, stream))
					}))
					defer server.Close()
					h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: target, BaseURL: server.URL + "/v1", APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
					if err != nil {
						t.Fatal(err)
					}
					body := map[string]any{"model": "public", "stream": stream, "max_tokens": 64, "messages": []any{map[string]any{"role": "user", "content": "lookup"}}}
					if inbound == "openai-responses" {
						delete(body, "messages")
						delete(body, "max_tokens")
						body["input"] = "lookup"
					}
					send := func() *httptest.ResponseRecorder {
						raw, _ := json.Marshal(body)
						w := httptest.NewRecorder()
						h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(inbound), strings.NewReader(string(raw))))
						return w
					}
					w := send()
					if w.Code != 200 || !strings.Contains(w.Body.String(), "call_1") || !strings.Contains(w.Body.String(), "call_2") || strings.Contains(w.Body.String(), `"type":"error"`) {
						t.Fatal(w.Code, w.Body.String())
					}
					if stream {
						if inbound == "openai-responses" && inbound != target && strings.Count(w.Body.String(), "event: response.function_call_arguments.done") != 2 {
							t.Fatal("missing tool completion events", w.Body.String())
						}
						return
					}
					var result map[string]any
					_ = json.Unmarshal(w.Body.Bytes(), &result)
					switch inbound {
					case "openai-responses":
						history := []any{map[string]any{"role": "user", "content": "lookup"}}
						history = append(history, result["output"].([]any)...)
						history = append(history, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "result-one"}, map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "result-two"})
						body["input"] = history
					case "openai-compatible":
						choice := result["choices"].([]any)[0].(map[string]any)
						body["messages"] = []any{map[string]any{"role": "user", "content": "lookup"}, choice["message"], map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "result-one"}, map[string]any{"role": "tool", "tool_call_id": "call_2", "content": "result-two"}}
					case "anthropic":
						body["messages"] = []any{map[string]any{"role": "user", "content": "lookup"}, map[string]any{"role": "assistant", "content": result["content"]}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "result-one"}, map[string]any{"type": "tool_result", "tool_use_id": "call_2", "content": "result-two"}}}}
					}
					w = send()
					if w.Code != 200 || calls != 2 {
						t.Fatal(w.Code, w.Body.String(), calls)
					}
				})
			}
		}
	}
}

func TestMultiUpstreamAuxiliaryAndLogs(t *testing.T) {
	for _, protocol := range []string{"openai-responses", "openai-compatible", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			dir := t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"model":"real"`) {
					t.Error(string(raw))
				}
				want := "/v1/responses/input_tokens"
				if protocol == "anthropic" {
					want = "/v1/messages/count_tokens"
				}
				if r.URL.Path != want && r.URL.Path != pathForTest(protocol) {
					t.Error(r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"input_tokens":7}`)
			}))
			defer server.Close()
			h, err := New(Config{RequestLogDir: dir, Upstreams: map[string]Upstream{"svc": {Protocol: protocol, BaseURL: server.URL + "/v1", APIKey: "secret-up", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}, {"second", "real-2"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/v1/models", "/v1/models/public", "/v1/models/second"} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != 200 || calls != 0 {
					t.Fatal(w.Code, calls)
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`)))
			if protocol == "openai-compatible" {
				if w.Code != 501 || calls != 0 {
					t.Fatal(w.Code, calls)
				}
			} else if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "7") {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			// Request logs should identify the selected upstream, while secrets are redacted.
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(protocol), strings.NewReader(`{"model":"public","input":"hi","messages":[{"role":"user","content":"hi"}]}`)))
			files, _ := os.ReadDir(dir)
			found := false
			for _, file := range files {
				if strings.HasSuffix(file.Name(), "_upstream.json") {
					raw, _ := os.ReadFile(filepath.Join(dir, file.Name()))
					if strings.Contains(string(raw), "secret-up") || !strings.Contains(string(raw), `"upstream": "svc"`) || !strings.Contains(string(raw), protocol) {
						t.Fatal(string(raw))
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing upstream log")
			}
		})
	}
}

func TestMultiUpstreamRejectsLossyRequests(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: "openai-compatible", BaseURL: server.URL, APIKey: "secret", Models: []Model{{"public", "real"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"model":"public","input":"hi","previous_response_id":"old"}`,
		`{"model":"public","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":32},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"public","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`,
	} {
		path := "/v1/messages"
		if strings.Contains(raw, "input") {
			path = "/v1/responses"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(raw)))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("unsupported requests reached upstream", calls)
	}
}

func TestMultiUpstreamFlushAndCancel(t *testing.T) {
	for _, protocol := range []string{"openai-compatible", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				source := streamForTest(protocol)
				cut := strings.Index(source, "\n\n") + 2
				fmt.Fprint(w, source[:cut])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: protocol, BaseURL: server.URL + "/v1", APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			gateway := httptest.NewServer(h)
			defer gateway.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", gateway.URL+"/v1/responses", strings.NewReader(`{"model":"public","input":"hi","stream":true}`))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(resp.Body).ReadString('\n')
			if err != nil || !strings.Contains(line, "response.created") {
				t.Fatal(line, err)
			}
			cancel()
			resp.Body.Close()
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream request was not canceled")
			}
		})
	}
}

func TestMultiUpstreamConcurrentRoutes(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["model"] == "real-a" && r.Header.Get("Authorization") != "Bearer key-a" {
			t.Error("route a contaminated")
		}
		if body["model"] == "real-b" && r.Header.Get("Authorization") != "Bearer key-b" {
			t.Error("route b contaminated")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	h, err := New(Config{Upstreams: map[string]Upstream{"a": {Protocol: "openai-responses", BaseURL: server.URL, APIKey: "key-a", Models: []Model{{"a", "real-a"}}}, "b": {Protocol: "openai-responses", BaseURL: server.URL, APIKey: "key-b", Models: []Model{{"b", "real-b"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			name := "a"
			if i%2 != 0 {
				name = "b"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+name+`","input":"hi"}`)))
			done <- w.Code == 200
		}(i)
	}
	for i := 0; i < 20; i++ {
		if !<-done {
			t.Fatal("request failed")
		}
	}
	if calls.Load() != 20 {
		t.Fatal(calls.Load())
	}
}

func TestMultiUpstreamProtocolMatrixJSON(t *testing.T) {
	protocols := []string{"openai-responses", "openai-compatible", "anthropic"}
	for _, target := range protocols {
		for _, inbound := range protocols {
			t.Run(inbound+"_to_"+target, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["model"] != "real-model" {
						t.Error(body)
					}
					if r.URL.Path != pathForTest(target) {
						t.Errorf("path %s", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					switch target {
					case "openai-responses":
						fmt.Fprint(w, `{"id":"resp_1","model":"real-model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`)
					case "openai-compatible":
						fmt.Fprint(w, `{"id":"chat_1","model":"real-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
					case "anthropic":
						fmt.Fprint(w, `{"id":"msg_1","model":"real-model","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
					}
				}))
				defer server.Close()
				h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: target, BaseURL: server.URL + "/v1", APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real-model"}}}}})
				if err != nil {
					t.Fatal(err)
				}
				raw := `{"model":"public","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
				if inbound == "openai-responses" {
					raw = `{"model":"public","max_output_tokens":64,"input":"hi"}`
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(inbound), strings.NewReader(raw)))
				if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") || calls != 1 {
					t.Fatal(w.Code, w.Body.String(), calls)
				}
				if inbound != target && !strings.Contains(w.Body.String(), `"model":"public"`) {
					t.Fatal("public model was not restored", w.Body.String())
				}
				var result map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				switch inbound {
				case "openai-responses":
					if result["output"] == nil {
						t.Fatal(result)
					}
				case "openai-compatible":
					if result["choices"] == nil {
						t.Fatal(result)
					}
				case "anthropic":
					if result["content"] == nil {
						t.Fatal(result)
					}
				}
			})
		}
	}
}

func pathForTest(protocol string) string {
	switch protocol {
	case "openai-responses":
		return "/v1/responses"
	case "openai-compatible":
		return "/v1/chat/completions"
	default:
		return "/v1/messages"
	}
}

func streamForTest(protocol string) string {
	switch protocol {
	case "openai-responses":
		return "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"real\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
	case "openai-compatible":
		return "data: {\"id\":\"chat_1\",\"model\":\"real\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
	default:
		return "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"real\",\"usage\":{\"input_tokens\":3}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	}
}

func TestMultiUpstreamProtocolMatrixSSE(t *testing.T) {
	for _, target := range []string{"openai-responses", "openai-compatible", "anthropic"} {
		for _, inbound := range []string{"openai-responses", "openai-compatible", "anthropic"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_to_%s/%t", inbound, target, stream), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, streamForTest(target))
					}))
					defer server.Close()
					h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: target, BaseURL: server.URL + "/v1", APIKey: "secret", AnthropicVersion: "2023-06-01", Models: []Model{{"public", "real"}}}}})
					if err != nil {
						t.Fatal(err)
					}
					raw := fmt.Sprintf(`{"model":"public","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
					if inbound == "openai-responses" {
						raw = fmt.Sprintf(`{"model":"public","stream":%t,"input":"hi"}`, stream)
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(inbound), strings.NewReader(raw)))
					if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
						t.Fatal(w.Code, w.Body.String())
					}
					if !stream {
						var obj map[string]any
						if json.Unmarshal(w.Body.Bytes(), &obj) != nil {
							t.Fatal(w.Body.String())
						}
						switch inbound {
						case "openai-responses":
							if obj["output"] == nil {
								t.Fatal(obj)
							}
						case "openai-compatible":
							if obj["choices"] == nil {
								t.Fatal(obj)
							}
						case "anthropic":
							if obj["content"] == nil {
								t.Fatal(obj)
							}
						}
					} else if inbound != target {
						terminal := map[string]string{"openai-responses": "response.completed", "openai-compatible": "[DONE]", "anthropic": "message_stop"}[inbound]
						if !strings.Contains(w.Body.String(), terminal) || strings.Contains(w.Body.String(), `"type":"error"`) || !strings.Contains(w.Body.String(), `"model":"public"`) {
							t.Fatal(w.Body.String())
						}
					}
				})
			}
		}
	}
}
