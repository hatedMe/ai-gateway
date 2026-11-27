package gateway

import (
	"ai-gateway-go/internal/translate"
	"reflect"
	"testing"
)

func TestModelCompatibilitySnapshot(t *testing.T) {
	caps := translate.ModelCapabilities{InputModalities: []string{"text", "image"}, Reasoning: &translate.ReasoningCapabilities{Mode: "budget", Efforts: []string{"low"}, Budgets: map[string]int{"low": 1024}}}
	router, err := newRouter(map[string]Upstream{"a": {Models: []Model{{Name: "public", Model: "real", Capabilities: caps}}}})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := router.Resolve("public")
	route.Capabilities.InputModalities[1] = "changed"
	route.Capabilities.Reasoning.Budgets["low"] = 2
	next, _ := router.Resolve("public")
	if next.Capabilities.InputModalities[1] != "image" || next.Capabilities.Reasoning.Budgets["low"] != 1024 {
		t.Fatal("request capabilities mutated shared route")
	}
}

func TestResolveModelRoute(t *testing.T) {
	upstreams := map[string]Upstream{"a": {Models: []Model{{Name: "code", Model: "real"}, {Name: "fast", Model: "fast-real"}}}, "b": {Models: []Model{{Name: "other-code", Model: "real"}}}}
	router, err := newRouter(upstreams)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Route{"code": {Upstream: "a", Model: "real"}, "fast": {Upstream: "a", Model: "fast-real"}, "other-code": {Upstream: "b", Model: "real"}} {
		got, err := router.Resolve(name)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	for _, name := range []string{"", "real", "CODE", "missing"} {
		if _, err := router.Resolve(name); err == nil {
			t.Fatal("accepted", name)
		}
	}
	upstreams["b"] = Upstream{Models: []Model{{Name: "code", Model: "real"}, {Name: "code", Model: "real"}}}
	if _, err := newRouter(upstreams); err == nil {
		t.Fatal("accepted duplicate name")
	}
}
