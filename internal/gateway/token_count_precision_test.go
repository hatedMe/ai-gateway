package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenCountPreservesLargeIntegers(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, limit := range []string{"", `,"max_tokens":128`} {
			t.Run(strings.Join([]string{map[bool]string{false: "legacy", true: "multi"}[multi], limit}, "/"), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					if strings.Contains(string(raw), "9007199254740992") || strings.Count(string(raw), "9007199254740993") != 2 {
						t.Errorf("integer changed: %s", raw)
					}
					var root map[string]json.RawMessage
					_ = json.Unmarshal(raw, &root)
					for _, field := range []string{"stream", "max_output_tokens", "store"} {
						if _, exists := root[field]; exists {
							t.Errorf("count request retained %s", field)
						}
					}
					if r.URL.Path != "/v1/responses/input_tokens" {
						t.Error(r.URL.Path)
					}
					_, _ = io.WriteString(w, `{"input_tokens":42}`)
				}))
				defer up.Close()
				cfg := Config{BaseURL: up.URL + "/v1", APIKey: "test"}
				if multi {
					cfg.Upstreams = map[string]Upstream{"test": {Protocol: "openai-responses", BaseURL: up.URL + "/v1", APIKey: "test", Models: []Model{{Name: "m", Model: "real"}}}}
				}
				h, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				body := `{"model":"m","stream":true,"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"id":{"enum":[9007199254740993]}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"id":9007199254740993}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]` + limit + `}`
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body)))
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"input_tokens":42`) {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}
