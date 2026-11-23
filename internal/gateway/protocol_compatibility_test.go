package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var compatibilityProtocols = []string{"openai-responses", "openai-compatible", "anthropic"}

func compatibilityHandler(t *testing.T, target string, serve http.HandlerFunc) http.Handler {
	t.Helper()
	up := httptest.NewServer(serve)
	t.Cleanup(up.Close)
	h, err := New(Config{Upstreams: map[string]Upstream{"svc": {Protocol: target, BaseURL: up.URL + "/v1", APIKey: "upstream-secret", AnthropicVersion: "2023-06-01", Models: []Model{{Name: "public", Model: "real"}, {Name: "second", Model: "real-second"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func compatibilitySend(t *testing.T, h http.Handler, source string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", pathForTest(source), strings.NewReader(string(raw))))
	return w
}

func compatibilityRequest(source string, stream bool, names []string) map[string]any {
	body := map[string]any{"model": "public", "stream": stream}
	schema := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}, "additionalProperties": false}
	tools := []any{}
	for _, name := range names {
		switch source {
		case "openai-responses":
			tools = append(tools, map[string]any{"type": "function", "name": name, "parameters": schema})
		case "openai-compatible":
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": schema}})
		case "anthropic":
			tools = append(tools, map[string]any{"name": name, "input_schema": schema})
		}
	}
	if source == "openai-responses" {
		body["input"] = "lookup"
		body["max_output_tokens"] = 64
		body["tool_choice"] = map[string]any{"type": "function", "name": names[0]}
	} else {
		body["messages"] = []any{map[string]any{"role": "user", "content": "lookup"}}
		body["max_tokens"] = 64
		if source == "anthropic" {
			body["tool_choice"] = map[string]any{"type": "tool", "name": names[0]}
		} else {
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": names[0]}}
		}
	}
	body["tools"] = tools
	return body
}

// Rebuild tool calls from actual client JSON or incremental SSE, without sorting
// the wire events or relying on escaped JSON substring comparisons.
func compatibilityCalls(t *testing.T, source string, stream bool, raw string) []map[string]any {
	t.Helper()
	var calls []map[string]any
	consume := func(root map[string]any) {
		switch source {
		case "openai-responses":
			if response, ok := root["response"].(map[string]any); ok {
				root = response
			}
			if output, ok := root["output"].([]any); ok {
				calls = nil
				for _, value := range output {
					item := value.(map[string]any)
					if item["type"] == "function_call" {
						calls = append(calls, map[string]any{"id": item["call_id"], "name": item["name"], "arguments": item["arguments"]})
					}
				}
			}
		case "openai-compatible":
			choices, _ := root["choices"].([]any)
			if len(choices) == 0 {
				return
			}
			choice := choices[0].(map[string]any)
			message, _ := choice["message"].(map[string]any)
			if stream {
				message, _ = choice["delta"].(map[string]any)
			}
			tools, _ := message["tool_calls"].([]any)
			for i, value := range tools {
				tool := value.(map[string]any)
				if stream {
					i = int(tool["index"].(float64))
				}
				for len(calls) <= i {
					calls = append(calls, map[string]any{"arguments": ""})
				}
				fn := tool["function"].(map[string]any)
				if id, ok := tool["id"]; ok {
					calls[i]["id"] = id
				}
				if name, ok := fn["name"]; ok {
					calls[i]["name"] = name
				}
				if args, ok := fn["arguments"].(string); ok {
					calls[i]["arguments"] = calls[i]["arguments"].(string) + args
				}
			}
		case "anthropic":
			blocks, _ := root["content"].([]any)
			if stream {
				if root["type"] == "content_block_start" {
					blocks = []any{root["content_block"]}
				}
				if delta, ok := root["delta"].(map[string]any); ok && delta["type"] == "input_json_delta" {
					i := int(root["index"].(float64))
					calls[i]["arguments"] = calls[i]["arguments"].(string) + delta["partial_json"].(string)
				}
			}
			for _, value := range blocks {
				block := value.(map[string]any)
				if block["type"] == "tool_use" {
					args := ""
					if !stream {
						encoded, _ := json.Marshal(block["input"])
						args = string(encoded)
					}
					calls = append(calls, map[string]any{"id": block["id"], "name": block["name"], "arguments": args})
				}
			}
		}
	}
	if !stream {
		var root map[string]any
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			t.Fatal(err, raw)
		}
		consume(root)
	} else {
		for _, line := range strings.Split(raw, "\n") {
			if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
				continue
			}
			var root map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &root); err != nil {
				t.Fatal(err, line)
			}
			if root["error"] != nil {
				t.Fatalf("unexpected stream error: %s", line)
			}
			consume(root)
		}
	}
	return calls
}

func compatibilityHistory(source string, calls []map[string]any) any {
	user := map[string]any{"role": "user", "content": "lookup"}
	if source == "openai-responses" {
		items := []any{user}
		for _, call := range calls {
			items = append(items, map[string]any{"type": "function_call", "call_id": call["id"], "name": call["name"], "arguments": call["arguments"]})
		}
		return append(items, map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "result-two"}, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "result-one"}, map[string]any{"role": "user", "content": "next-user"})
	}
	blocks := []any{}
	for _, call := range calls {
		if source == "openai-compatible" {
			blocks = append(blocks, map[string]any{"id": call["id"], "type": "function", "function": map[string]any{"name": call["name"], "arguments": call["arguments"]}})
		} else {
			var args any
			_ = json.Unmarshal([]byte(call["arguments"].(string)), &args)
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call["id"], "name": call["name"], "input": args})
		}
	}
	if source == "openai-compatible" {
		return []any{user, map[string]any{"role": "assistant", "content": nil, "tool_calls": blocks}, map[string]any{"role": "tool", "tool_call_id": "call_2", "content": "result-two"}, map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "result-one"}, map[string]any{"role": "user", "content": "next-user"}}
	}
	return []any{user, map[string]any{"role": "assistant", "content": blocks}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_2", "content": "result-two"}, map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "result-one"}, map[string]any{"type": "text", "text": "next-user"}}}}
}

func TestProtocolCompatibilityToolNamesRoundTrip(t *testing.T) {
	long := "mcp/server." + strings.Repeat("lookup", 16)
	sum := sha256.Sum256([]byte(long))
	reserved := fmt.Sprintf("tool_%x", sum[:])[:64]
	names := []string{long, reserved}
	for _, source := range compatibilityProtocols {
		for _, target := range compatibilityProtocols {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_to_%s/stream=%t", source, target, stream), func(t *testing.T) {
					requests := 0
					var firstNames []string
					h := compatibilityHandler(t, target, func(w http.ResponseWriter, r *http.Request) {
						requests++
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						if body["model"] != "real" || r.URL.Path != pathForTest(target) {
							t.Error("incorrect route", body["model"], r.URL.Path)
						}
						tools, _ := body["tools"].([]any)
						wireNames := []string{}
						for _, value := range tools {
							tool := value.(map[string]any)
							if target == "openai-compatible" {
								tool = tool["function"].(map[string]any)
							}
							wireNames = append(wireNames, tool["name"].(string))
						}
						if len(wireNames) != 2 || wireNames[0] == wireNames[1] {
							t.Error("colliding or missing tool definitions", wireNames)
							w.WriteHeader(500)
							return
						}
						if requests == 1 {
							firstNames = wireNames
						} else if strings.Join(firstNames, ",") != strings.Join(wireNames, ",") {
							t.Error("tool mapping changed across rounds", firstNames, wireNames)
						}
						choice, _ := body["tool_choice"].(map[string]any)
						if target == "openai-compatible" {
							choice, _ = choice["function"].(map[string]any)
						}
						if choice["name"] != wireNames[0] {
							t.Error("tool_choice does not use definition mapping", choice, wireNames)
						}
						if requests == 2 {
							raw, _ := json.Marshal(body)
							text := string(raw)
							if a, b, c := strings.Index(text, "result-two"), strings.Index(text, "result-one"), strings.Index(text, "next-user"); a < 0 || b <= a || c <= b {
								t.Error("tool results or following user text reordered", text)
							}
						}
						fixture := toolFixture(target, stream)
						for _, name := range wireNames {
							encoded, _ := json.Marshal(name)
							fixture = strings.Replace(fixture, `"lookup"`, string(encoded), 1)
						}
						w.Header().Set("Content-Type", "application/json")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						}
						fmt.Fprint(w, fixture)
					})
					body := compatibilityRequest(source, stream, names)
					for round := 0; round < 2; round++ {
						w := compatibilitySend(t, h, source, body)
						if w.Code != 200 {
							t.Fatal(w.Code, w.Body.String())
						}
						calls := compatibilityCalls(t, source, stream, w.Body.String())
						if len(calls) != 2 {
							t.Fatal("missing calls", w.Body.String())
						}
						for i, call := range calls {
							var args map[string]any
							if call["id"] != fmt.Sprintf("call_%d", i+1) || call["name"] != names[i] || json.Unmarshal([]byte(call["arguments"].(string)), &args) != nil || args["q"] != []string{"one", "two"}[i] {
								t.Fatal("tool identity or arguments changed", calls)
							}
						}
						key := "messages"
						if source == "openai-responses" {
							key = "input"
						}
						body[key] = compatibilityHistory(source, calls)
					}
					if requests != 2 {
						t.Fatal("unexpected request count", requests)
					}
				})
			}
		}
	}
}

func TestProtocolCompatibilityInvalidHistoryNoOutbound(t *testing.T) {
	for _, source := range compatibilityProtocols {
		for _, target := range compatibilityProtocols {
			if source == target {
				continue
			}
			for _, kind := range []string{"orphan", "duplicate", "missing"} {
				t.Run(source+"_to_"+target+"/"+kind, func(t *testing.T) {
					var outbound atomic.Int32
					h := compatibilityHandler(t, target, func(w http.ResponseWriter, _ *http.Request) { outbound.Add(1); w.WriteHeader(500) })
					calls := []map[string]any{{"id": "call_1", "name": "lookup", "arguments": `{}`}, {"id": "call_2", "name": "lookup", "arguments": `{}`}}
					if kind == "duplicate" {
						calls[1]["id"] = "call_1"
					}
					body := compatibilityRequest(source, false, []string{"lookup"})
					key := "messages"
					if source == "openai-responses" {
						key = "input"
					}
					items := compatibilityHistory(source, calls).([]any)
					if kind == "orphan" {
						items = append(items[:1], items[2:]...)
						if source == "openai-responses" {
							items = []any{map[string]any{"type": "function_call_output", "call_id": "orphan", "output": "private-tool-result"}}
						}
					} else if kind == "missing" {
						if source == "openai-responses" {
							items = items[:3]
						} else {
							items = items[:2]
						}
					}
					body[key] = items
					w := compatibilitySend(t, h, source, body)
					if w.Code != 400 || outbound.Load() != 0 {
						t.Fatal("invalid history must fail before outbound", w.Code, outbound.Load(), w.Body.String())
					}
				})
			}
		}
	}
}

func TestProtocolCompatibilityStructuredErrors(t *testing.T) {
	for _, source := range compatibilityProtocols {
		for _, usage := range []string{`{"prompt_tokens":-1}`, `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":1}`, `{"prompt_tokens":9223372036854775807,"completion_tokens":1}`} {
			for _, stream := range []bool{false, true} {
				if source == "openai-compatible" {
					continue // This test exercises upstream conversion, not native forwarding.
				}
				t.Run(fmt.Sprintf("%s/%s/stream=%t", source, usage, stream), func(t *testing.T) {
					h := compatibilityHandler(t, "openai-compatible", func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("x-request-id", "usage-request")
						w.Header().Set("Content-Type", "application/json")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: {\"id\":\"c\",\"choices\":[],\"usage\":%s}\n\n", usage)
							return
						}
						fmt.Fprintf(w, `{"id":"c","choices":[{"message":{"content":"private-upstream-content"},"finish_reason":"stop"}],"usage":%s}`, usage)
					})
					w := compatibilitySend(t, h, source, compatibilityRequest(source, stream, []string{"lookup"}))
					wantStatus, stage := 502, "response"
					if stream {
						wantStatus, stage = 200, "stream"
					}
					if w.Code != wantStatus || !strings.Contains(w.Body.String(), `"code":"invalid_usage"`) || !strings.Contains(w.Body.String(), `"stage":"`+stage+`"`) || w.Header().Get("request-id") != "usage-request" {
						t.Errorf("lost conversion diagnostics or upstream request ID: %d %v %s", w.Code, w.Header(), w.Body.String())
					}
					if strings.Contains(w.Body.String(), "private-upstream-content") || strings.Contains(w.Body.String(), "upstream-secret") || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "response.completed") || strings.Contains(w.Body.String(), "message_stop") {
						t.Error("error leaked private content or emitted success terminal", w.Body.String())
					}
				})
			}
		}
	}
}

func TestProtocolCompatibilityNativeExtensions(t *testing.T) {
	for _, protocol := range compatibilityProtocols {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				body := compatibilityRequest(protocol, stream, []string{"lookup"})
				body["provider_extension"] = map[string]any{"opaque": []any{"keep", true}, "signature": "native-only"}
				response := `{"model":"real-native","native_extension":{"untouched":true}}`
				if stream {
					response = "event: vendor.native\ndata: {\"opaque\":\"native-only\"}\n\ndata: [DONE]\n\n"
				}
				h := compatibilityHandler(t, protocol, func(w http.ResponseWriter, r *http.Request) {
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					expectedRaw, _ := json.Marshal(body)
					var expected map[string]any
					_ = json.Unmarshal(expectedRaw, &expected)
					expected["model"] = "real"
					if !reflect.DeepEqual(got, expected) {
						t.Error("native fields changed", got, expected)
					}
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, response)
				})
				w := compatibilitySend(t, h, protocol, body)
				if w.Code != 200 || w.Body.String() != response {
					t.Fatal("native response was not preserved", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestProtocolCompatibilityRequestErrorSafety(t *testing.T) {
	for _, source := range compatibilityProtocols {
		for _, target := range []string{"openai-compatible", "anthropic"} {
			if source == target {
				continue
			}
			t.Run(source+"_to_"+target, func(t *testing.T) {
				var outbound atomic.Int32
				h := compatibilityHandler(t, target, func(w http.ResponseWriter, _ *http.Request) { outbound.Add(1); w.WriteHeader(500) })
				body := compatibilityRequest(source, false, []string{"lookup"})
				body["private_field"] = "private-client-payload"
				w := compatibilitySend(t, h, source, body)
				var result struct {
					Error struct{ Code, Stage, Field string }
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err, w.Body.String())
				}
				if w.Code != 400 || outbound.Load() != 0 || result.Error.Code != "unsupported_field" || result.Error.Stage != "request" || result.Error.Field != "private_field" {
					t.Error("missing safe structured request diagnostics", w.Code, outbound.Load(), w.Body.String())
				}
				if strings.Contains(w.Body.String(), "private-client-payload") || strings.Contains(w.Body.String(), "upstream-secret") {
					t.Error("private value appeared in error", w.Body.String())
				}
			})
		}
	}
}
