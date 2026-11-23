package main

import (
	"ai-gateway-go/internal/config"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var advancedProtocols = []string{"openai-compatible", "openai-responses", "anthropic"}

func TestConfiguredAdvancedImages(t *testing.T) {
	for _, source := range advancedProtocols {
		for _, target := range advancedProtocols {
			if source == target {
				continue
			}
			t.Run(source+"/"+target, func(t *testing.T) {
				h, calls, captured := advancedConfiguredHandler(t, target)
				for _, mixed := range []bool{false, true} {
					body := advancedRequest(source, "enabled")
					content := []any{advancedImage(source)}
					if mixed {
						content = []any{advancedText(source, "before"), advancedImage(source), advancedText(source, "after")}
					}
					advancedSetMessages(body, source, []any{map[string]any{"role": "user", "content": content}})
					advancedSend(t, h, source, body, http.StatusOK)
					out := <-captured
					want := []any{advancedImage(target)}
					if mixed {
						want = []any{advancedText(target, "before"), advancedImage(target), advancedText(target, "after")}
					}
					messages := advancedMessages(t, out, target)
					if len(messages) != 1 || !reflect.DeepEqual(messages[0].(map[string]any)["content"], want) {
						t.Fatalf("image content changed: %#v", out)
					}
				}
				prior := calls.Load()
				body := advancedRequest(source, "plain")
				advancedSetMessages(body, source, []any{map[string]any{"role": "user", "content": []any{advancedImage(source)}}})
				advancedSend(t, h, source, body, http.StatusBadRequest)
				if calls.Load() != prior {
					t.Fatal("disabled image capability accessed upstream")
				}
			})
		}
	}
}

func TestConfiguredAdvancedJSONSchema(t *testing.T) {
	for _, source := range advancedProtocols {
		for _, target := range advancedProtocols {
			if source == target {
				continue
			}
			t.Run(source+"/"+target, func(t *testing.T) {
				h, calls, captured := advancedConfiguredHandler(t, target)
				body := advancedRequest(source, "enabled")
				advancedSetFormat(body, source)
				advancedSend(t, h, source, body, http.StatusOK)
				out := <-captured
				format := advancedGetFormat(t, out, target)
				if format["type"] != "json_schema" {
					t.Fatalf("missing native format: %#v", out)
				}
				if target == "openai-compatible" {
					format = format["json_schema"].(map[string]any)
				}
				if !reflect.DeepEqual(format["schema"], advancedSchema()) {
					t.Fatalf("schema changed: %#v", format)
				}
				if target != "anthropic" && (format["strict"] != true || format["name"] != "response") {
					t.Fatalf("strict/name lost: %#v", format)
				}
				prior := calls.Load()
				body["model"] = "plain"
				advancedSend(t, h, source, body, http.StatusBadRequest)
				if calls.Load() != prior {
					t.Fatal("disabled schema capability accessed upstream")
				}
			})
		}
	}
}

func TestConfiguredAdvancedReasoning(t *testing.T) {
	for _, source := range advancedProtocols {
		for _, target := range advancedProtocols {
			if source == target {
				continue
			}
			t.Run(source+"/"+target, func(t *testing.T) {
				h, calls, captured := advancedConfiguredHandler(t, target)
				body := advancedRequest(source, "enabled")
				advancedSetEffort(body, source, "medium")
				advancedSend(t, h, source, body, http.StatusOK)
				out := <-captured
				switch target {
				case "openai-compatible":
					if out["reasoning_effort"] != "medium" {
						t.Fatalf("effort missing: %#v", out)
					}
				case "openai-responses":
					if !reflect.DeepEqual(out["reasoning"], map[string]any{"effort": "medium"}) {
						t.Fatalf("effort missing: %#v", out)
					}
				case "anthropic":
					if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled", "budget_tokens": float64(1024)}) {
						t.Fatalf("budget missing: %#v", out)
					}
				}
				prior := calls.Load()
				body["model"] = "plain"
				if target == "openai-responses" {
					// Responses preserves legacy reasoning fallback unless a route overrides it.
					advancedSend(t, h, source, body, http.StatusOK)
					<-captured
					prior = calls.Load()
					body["model"] = "restricted"
				}
				advancedSend(t, h, source, body, http.StatusBadRequest)
				body["model"] = "enabled"
				advancedSetEffort(body, source, "high")
				advancedSend(t, h, source, body, http.StatusBadRequest)
				if source == "anthropic" {
					delete(body, "output_config")
					body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 1024}
					advancedSend(t, h, source, body, http.StatusBadRequest)
				}
				if target == "anthropic" {
					advancedSetEffort(body, source, "medium")
					if source == "openai-compatible" {
						body["max_tokens"] = 1024
					} else {
						body["max_output_tokens"] = 1024
					}
					advancedSend(t, h, source, body, http.StatusBadRequest)
				}
				if calls.Load() != prior {
					t.Fatal("unsupported reasoning accessed upstream")
				}
			})
		}
	}
}

func TestConfiguredAdvancedNativePassthrough(t *testing.T) {
	for _, protocol := range advancedProtocols {
		t.Run(protocol, func(t *testing.T) {
			h, _, captured := advancedConfiguredHandler(t, protocol)
			body := advancedRequest(protocol, "plain")
			advancedSetMessages(body, protocol, []any{map[string]any{"role": "user", "content": []any{advancedImage(protocol)}}})
			advancedSetFormat(body, protocol)
			advancedSetEffort(body, protocol, "high")
			body["provider_extension"] = map[string]any{"preserve": true}
			advancedSend(t, h, protocol, body, http.StatusOK)
			out := <-captured
			body["model"] = "real-model"
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			var normalized map[string]any
			if err := json.Unmarshal(raw, &normalized); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out, normalized) {
				t.Fatalf("native fields changed: got %#v, want %#v", out, normalized)
			}
		})
	}
}

func advancedConfiguredHandler(t *testing.T, target string) (http.Handler, *atomic.Int32, chan map[string]any) {
	t.Helper()
	setupEnv(t)
	calls := new(atomic.Int32)
	captured := make(chan map[string]any, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		captured <- body
		if r.URL.Path != advancedPath(target) || body["model"] != "real-model" {
			t.Errorf("incorrect upstream route: %s %#v", r.URL.Path, body)
		}
		if target == "anthropic" {
			if r.Header.Get("X-Api-Key") != "test-key" {
				t.Error("missing Anthropic credential")
			}
		} else if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing upstream credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch target {
		case "openai-compatible":
			fmt.Fprint(w, `{"id":"chat_test","model":"real-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		case "openai-responses":
			fmt.Fprint(w, `{"id":"resp_test","status":"completed","model":"real-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
		case "anthropic":
			fmt.Fprint(w, `{"id":"msg_test","type":"message","role":"assistant","model":"real-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	reasoning := "mode: effort\n            efforts: [none, medium]"
	restrictedMode := "effort"
	version := ""
	if target == "anthropic" {
		restrictedMode = "budget"
		reasoning = "mode: budget\n            efforts: [none, medium]\n            budgets: {medium: 1024}"
		version = "    anthropic_version: \"2023-06-01\"\n"
	}
	path := saveConfig(t, fmt.Sprintf(`upstreams:
  advanced:
    protocol: %s
    base_url: %q
    api_key_env: OPENAI_API_KEY
%s    models:
      - name: enabled
        model: real-model
        compatibility:
          input_modalities: [text, image]
          json_schema: true
          reasoning:
            %s
      - name: plain
        model: real-model
      - name: restricted
        model: real-model
        compatibility:
          reasoning:
            mode: %s
            efforts: [none]
`, target, upstream.URL+"/v1", version, reasoning, restrictedMode))
	c, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newServer(c)
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler, calls, captured
}

func advancedRequest(source, model string) map[string]any {
	body := map[string]any{"model": model}
	advancedSetMessages(body, source, []any{map[string]any{"role": "user", "content": []any{advancedText(source, "hello")}}})
	if source == "openai-responses" {
		body["max_output_tokens"] = 4096
	} else {
		body["max_tokens"] = 4096
	}
	return body
}

func advancedPath(protocol string) string {
	switch protocol {
	case "openai-compatible":
		return "/v1/chat/completions"
	case "openai-responses":
		return "/v1/responses"
	default:
		return "/v1/messages"
	}
}

func advancedSetMessages(body map[string]any, protocol string, messages []any) {
	key := "messages"
	if protocol == "openai-responses" {
		key = "input"
	}
	body[key] = messages
}

func advancedMessages(t *testing.T, body map[string]any, protocol string) []any {
	t.Helper()
	key := "messages"
	if protocol == "openai-responses" {
		key = "input"
	}
	messages, ok := body[key].([]any)
	if !ok {
		t.Fatalf("missing target history: %#v", body)
	}
	return messages
}

func advancedText(protocol, text string) map[string]any {
	typ := "text"
	if protocol == "openai-responses" {
		typ = "input_text"
	}
	return map[string]any{"type": typ, "text": text}
}

func advancedImage(protocol string) map[string]any {
	switch protocol {
	case "openai-compatible":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}
	case "openai-responses":
		return map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"}
	default:
		return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.test/image.png"}}
	}
}

func advancedSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []any{"answer"}, "additionalProperties": false}
}

func advancedSetFormat(body map[string]any, protocol string) {
	format := map[string]any{"type": "json_schema", "schema": advancedSchema()}
	if protocol != "anthropic" {
		format["name"] = "response"
		format["strict"] = true
	}
	switch protocol {
	case "openai-compatible":
		delete(format, "type")
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": format}
	case "openai-responses":
		body["text"] = map[string]any{"format": format}
	case "anthropic":
		body["output_config"] = map[string]any{"format": format}
	}
}

func advancedGetFormat(t *testing.T, body map[string]any, protocol string) map[string]any {
	t.Helper()
	var format any
	switch protocol {
	case "openai-compatible":
		format = body["response_format"]
	case "openai-responses":
		if text, ok := body["text"].(map[string]any); ok {
			format = text["format"]
		}
	case "anthropic":
		if output, ok := body["output_config"].(map[string]any); ok {
			format = output["format"]
		}
	}
	result, ok := format.(map[string]any)
	if !ok {
		t.Fatalf("missing target format: %#v", body)
	}
	return result
}

func advancedSetEffort(body map[string]any, protocol, effort string) {
	switch protocol {
	case "openai-compatible":
		body["reasoning_effort"] = effort
	case "openai-responses":
		body["reasoning"] = map[string]any{"effort": effort}
	case "anthropic":
		body["output_config"] = map[string]any{"effort": effort}
	}
}

func advancedSend(t *testing.T, handler http.Handler, source string, body map[string]any, status int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, advancedPath(source), strings.NewReader(string(raw))))
	if w.Code != status {
		t.Fatalf("status=%d, want=%d: %s", w.Code, status, w.Body.String())
	}
}
