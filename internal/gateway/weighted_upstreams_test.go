package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-go/internal/translate"
)

func TestWeightedAliasHTTPRoutes(t *testing.T) {
	for _, source := range compatibilityProtocols {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", source, stream), func(t *testing.T) {
				counts := map[string]int{}
				selected := []string{}
				serve := func(key string) http.HandlerFunc {
					return func(w http.ResponseWriter, r *http.Request) {
						var body struct{ Model string }
						_ = json.NewDecoder(r.Body).Decode(&body)
						if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
							t.Error("candidate transport mixed")
						}
						counts[key+"/"+body.Model]++
						selected = append(selected, key+"/"+body.Model)
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, streamForTest("openai-compatible"))
						} else {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprintf(w, `{"id":"c","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, body.Model)
						}
					}
				}
				a, b := httptest.NewServer(serve("a")), httptest.NewServer(serve("b"))
				defer a.Close()
				defer b.Close()
				h, err := New(Config{Client: &http.Client{Transport: &http.Transport{}}, Upstreams: map[string]Upstream{"a": {Protocol: "openai-compatible", BaseURL: a.URL + "/v1", APIKey: "a", Models: []Model{{Name: "public", Model: "real-a", Weight: 3}, {Name: "public", Model: "real-b", Weight: 1}}}, "b": {Protocol: "openai-compatible", BaseURL: b.URL + "/v1", APIKey: "b", Models: []Model{{Name: "public", Model: "real-a", Weight: 2}}}}})
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"/v1/models", "/v1/models/public", "/v1/models?limit=1"} {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
					if w.Code != 200 {
						t.Fatal(w.Code)
					}
					if path == "/v1/models" {
						var catalog struct{ Data []struct{ ID string } }
						_ = json.Unmarshal(w.Body.Bytes(), &catalog)
						if len(catalog.Data) != 1 || catalog.Data[0].ID != "public" {
							t.Fatal(w.Body.String())
						}
					}
				}
				sequence := []string{"a/real-a", "b/real-a", "a/real-a", "a/real-b", "b/real-a", "a/real-a"}
				for i, want := range sequence {
					w := compatibilitySend(t, h, source, compatibilityRequest(source, stream, []string{"lookup"}))
					if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") || strings.Contains(w.Body.String(), `"type":"error"`) {
						t.Fatal(w.Code, w.Body.String())
					}
					if source != "openai-compatible" && !strings.Contains(w.Body.String(), `"model":"public"`) {
						t.Fatal("alias not restored")
					}
					if len(selected) != i+1 || selected[i] != want {
						t.Fatalf("request %d: got %v, want candidate %s", i, selected, want)
					}
				}
				if !reflect.DeepEqual(counts, map[string]int{"a/real-a": 3, "a/real-b": 1, "b/real-a": 2}) {
					t.Fatal(counts)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"real-a","input":"hi"}`)))
				if w.Code != 400 {
					t.Fatal("real ID unexpectedly published")
				}
			})
		}
	}
}

func TestWeightedAliasFailureDoesNotRetry(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(502)
		fmt.Fprint(w, `{"error":{"message":"unavailable"}}`)
	}))
	defer up.Close()
	h, err := New(Config{Client: &http.Client{Transport: &http.Transport{}}, Upstreams: map[string]Upstream{"a": {Protocol: "openai-responses", BaseURL: up.URL, APIKey: "test", Models: []Model{{Name: "public", Model: "first"}, {Name: "public", Model: "second"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := compatibilitySend(t, h, "openai-responses", compatibilityRequest("openai-responses", false, []string{"lookup"}))
	if w.Code != 502 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	route, _ := h.(*handler).routes.Resolve("public")
	if route.Model != "second" {
		t.Fatal("failed request refunded or retried", route)
	}
}

func TestWeightedAliasCountTokens(t *testing.T) {
	calls := []string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		fmt.Fprint(w, `{"input_tokens":3}`)
	}))
	defer up.Close()
	ups := map[string]Upstream{}
	for i, p := range compatibilityProtocols {
		ups[fmt.Sprint(i)] = Upstream{Protocol: p, BaseURL: up.URL + "/v1", APIKey: "test", AnthropicVersion: "2023-06-01", Models: []Model{{Name: "public", Model: "real"}}}
	}
	h, err := New(Config{Client: &http.Client{Transport: &http.Transport{}}, Upstreams: ups})
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []int{200, 200, 200, 200, 501, 200} {
		w := httptest.NewRecorder()
		path := "/v1/messages/count_tokens"
		if i == 1 {
			path = "/v1/chat/completions"
		}
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`)))
		if w.Code != status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(calls, []string{"/v1/responses/input_tokens", "/v1/chat/completions", "/v1/messages/count_tokens", "/v1/responses/input_tokens", "/v1/messages/count_tokens"}) {
		t.Fatal(calls)
	}
}

func TestWeightedAliasCapabilitiesStayIsolated(t *testing.T) {
	fields := []string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, k := range []string{"max_tokens", "max_completion_tokens"} {
			if body[k] != nil {
				fields = append(fields, k)
			}
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer up.Close()
	h, err := New(Config{Client: &http.Client{Transport: &http.Transport{}}, Upstreams: map[string]Upstream{"a": {Protocol: "openai-compatible", BaseURL: up.URL, APIKey: "test", Models: []Model{{Name: "public", Model: "a", Capabilities: translate.ModelCapabilities{ChatTokenLimitField: "max_tokens"}}, {Name: "public", Model: "b", Capabilities: translate.ModelCapabilities{ChatTokenLimitField: "max_completion_tokens"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public","max_completion_tokens":20,"messages":[]}`)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(fields, []string{"max_tokens", "max_completion_tokens"}) {
		t.Fatal(fields)
	}
}

func TestWeightedAliasUnsupportedCapabilityDoesNotSwitch(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":"c","model":"real","choices":[{"index":0,"message":{"content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	h, err := New(Config{Client: up.Client(), Upstreams: map[string]Upstream{"a": {Protocol: "openai-compatible", BaseURL: up.URL, APIKey: "test", Models: []Model{{Name: "public", Model: "text"}, {Name: "public", Model: "vision", Capabilities: translate.ModelCapabilities{InputModalities: []string{"text", "image"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{400, 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`)))
		if w.Code != status {
			t.Fatal(w.Code, w.Body.String())
		}
		if status == 400 && calls != 0 {
			t.Fatal("unsupported candidate switched")
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
