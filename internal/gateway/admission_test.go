package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayAdmissionLimit(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer up.Close()
	h, err := New(Config{MaxConcurrentRequests: 1, Upstreams: map[string]Upstream{
		"a": {Protocol: "openai-responses", BaseURL: up.URL, APIKey: "test", Models: []Model{{Name: "a", Model: "real"}}},
		"b": {Protocol: "openai-responses", BaseURL: up.URL, APIKey: "test", Models: []Model{{Name: "b", Model: "real"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"a","input":"hi"}`)).WithContext(ctx))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request not admitted")
	}
	body := &forbiddenBody{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", body))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || body.reads != 0 {
		t.Fatalf("status=%d reads=%d", w.Code, body.reads)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 200 {
		t.Fatal("local models consumed admission slot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request not released")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream not canceled")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"unknown"}`)))
	if w.Code != 400 {
		t.Fatal("admission slot leaked", w.Code)
	}
}
