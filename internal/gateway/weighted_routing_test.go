package gateway

import (
	"reflect"
	"sync"
	"testing"
)

func weightedFixture(t *testing.T) Router {
	t.Helper()
	r, err := newRouter(map[string]Upstream{"a": {Models: []Model{{Name: "coding", Model: "a", Weight: 3}, {Name: "coding", Model: "b", Weight: 1}, {Name: "other", Model: "a"}}}, "b": {Models: []Model{{Name: "coding", Model: "a", Weight: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestWeightedRouteDistribution(t *testing.T) {
	r := weightedFixture(t)
	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		route, err := r.Resolve("coding")
		if err != nil {
			t.Fatal(err)
		}
		counts[route.Upstream+"/"+route.Model]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"a/a": 300, "a/b": 100, "b/a": 200}) {
		t.Fatal(counts)
	}
}
func TestWeightedRouteConcurrentDistribution(t *testing.T) {
	r := weightedFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			route, err := r.Resolve("coding")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			counts[route.Upstream+"/"+route.Model]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(counts, map[string]int{"a/a": 300, "a/b": 100, "b/a": 200}) {
		t.Fatal(counts)
	}
}
func TestWeightedRouteOrderAndIsolation(t *testing.T) {
	r, s := weightedFixture(t), weightedFixture(t)
	for i := 0; i < 30; i++ {
		_, _ = r.Resolve("other")
		a, _ := r.Resolve("coding")
		b, _ := s.Resolve("coding")
		if !reflect.DeepEqual(a, b) {
			t.Fatal("unstable or shared state", a, b)
		}
	}
	if _, err := newRouter(map[string]Upstream{"a": {Models: []Model{{Name: "x", Model: "a"}, {Name: "x", Model: "a", Weight: 2}}}}); err == nil {
		t.Fatal("duplicate candidate accepted")
	}
	for _, w := range []int64{-1, 1000001} {
		if _, err := newRouter(map[string]Upstream{"a": {Models: []Model{{Name: "x", Model: "a", Weight: w}}}}); err == nil {
			t.Fatal("invalid weight accepted")
		}
	}
}
