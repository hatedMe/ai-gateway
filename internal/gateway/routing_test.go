package gateway

import "testing"

func TestResolveModelRoute(t *testing.T) {
	upstreams := map[string]Upstream{"a": {Models: []Model{{Name: "code", Model: "real"}, {Name: "fast", Model: "fast-real"}}}, "b": {Models: []Model{{Name: "other-code", Model: "real"}}}}
	router, err := newRouter(upstreams)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Route{"code": {"a", "real"}, "fast": {"a", "fast-real"}, "other-code": {"b", "real"}} {
		got, err := router.Resolve(name)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	for _, name := range []string{"", "real", "CODE", "missing"} {
		if _, err := router.Resolve(name); err == nil {
			t.Fatal("accepted", name)
		}
	}
	upstreams["b"] = Upstream{Models: []Model{{Name: "code", Model: "real"}}}
	if _, err := newRouter(upstreams); err == nil {
		t.Fatal("accepted duplicate name")
	}
}
