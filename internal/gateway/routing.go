package gateway

import (
	"ai-gateway-go/internal/translate"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type Model struct {
	Weight       int64
	Name, Model  string
	Capabilities translate.ModelCapabilities
}
type Upstream struct {
	ResponseHeaderTimeout, ReadIdleTimeout                time.Duration
	Protocol, BaseURL, APIKey, ProxyURL, AnthropicVersion string
	Models                                                []Model
}
type Route struct {
	Upstream, Model string
	Capabilities    translate.ModelCapabilities
}
type weightedRoute struct {
	route           Route
	weight, current int64
}
type modelPool struct {
	mu         sync.Mutex
	candidates []weightedRoute
	total      int64
}
type Router map[string]*modelPool

func newRouter(upstreams map[string]Upstream) (Router, error) {
	routes := Router{}
	names := make([]string, 0, len(upstreams))
	for name := range upstreams {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		seen := map[[2]string]bool{}
		for _, m := range upstreams[name].Models {
			if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Model) == "" {
				return nil, fmt.Errorf("model names must not be empty")
			}
			key := [2]string{m.Name, m.Model}
			if seen[key] {
				return nil, fmt.Errorf("duplicate model candidate")
			}
			seen[key] = true
			weight := m.Weight
			if weight == 0 {
				weight = 1
			}
			if weight < 1 || weight > 1000000 {
				return nil, fmt.Errorf("model weight must be between 1 and 1000000")
			}
			pool := routes[m.Name]
			if pool == nil {
				pool = &modelPool{}
				routes[m.Name] = pool
			}
			if pool.total > math.MaxInt64-weight {
				return nil, fmt.Errorf("total model weight overflow")
			}
			pool.total += weight
			pool.candidates = append(pool.candidates, weightedRoute{route: Route{Upstream: name, Model: m.Model, Capabilities: m.Capabilities.Snapshot()}, weight: weight})
		}
	}
	return routes, nil
}
func (r Router) Resolve(model string) (Route, error) {
	pool, ok := r[model]
	if !ok {
		return Route{}, fmt.Errorf("model is not configured")
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	best := 0
	for i := range pool.candidates {
		pool.candidates[i].current += pool.candidates[i].weight
		if pool.candidates[i].current > pool.candidates[best].current {
			best = i
		}
	}
	pool.candidates[best].current -= pool.total
	route := pool.candidates[best].route
	route.Capabilities = route.Capabilities.Snapshot()
	return route, nil
}
