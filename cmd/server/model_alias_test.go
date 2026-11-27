package main

import (
	"ai-gateway-go/internal/config"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredModelAlias(t *testing.T) {
	setupEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "real" {
			t.Error("incorrect actual model", body.Model)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer up.Close()
	for _, entry := range []string{"name: real\n        alias: public", "name: public\n        model: real"} {
		body := fmt.Sprintf("upstreams:\n  svc:\n    protocol: openai-responses\n    base_url: %q\n    api_key_env: OPENAI_API_KEY\n    models:\n      - %s\n", up.URL, entry)
		c, err := config.Load(saveConfig(t, body), true)
		if err != nil {
			t.Fatal(err)
		}
		s, err := newServer(c)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi"}`)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestConfiguredWeightedModelAlias(t *testing.T) {
	setupEnv(t)
	counts := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		counts[body.Model]++
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer up.Close()
	body := fmt.Sprintf("upstreams:\n  svc:\n    protocol: openai-responses\n    base_url: %q\n    api_key_env: OPENAI_API_KEY\n    models:\n      - name: real-a\n        alias: public\n        weight: 3\n      - name: real-b\n        alias: public\n        weight: 1\n", up.URL)
	path := saveConfig(t, body)
	if err := run([]string{"--config", path, "--check-config"}); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Fatal("check config called upstream")
	}
	c, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newServer(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi"}`)))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if counts["real-a"] != 3 || counts["real-b"] != 1 {
		t.Fatal(counts)
	}
}
