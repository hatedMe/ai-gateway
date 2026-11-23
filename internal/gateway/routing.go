package gateway

import (
	"ai-gateway-go/internal/translate"
	"fmt"
)

type Model struct {
	Name, Model  string
	Capabilities translate.ModelCapabilities
}
type Upstream struct {
	Protocol, BaseURL, APIKey, ProxyURL, AnthropicVersion string
	Models                                                []Model
}
type Route struct {
	Upstream, Model string
	Capabilities    translate.ModelCapabilities
}
type Router map[string]Route

func newRouter(upstreams map[string]Upstream) (Router, error) {
	routes := Router{}
	for name, u := range upstreams {
		for _, m := range u.Models {
			if _, ok := routes[m.Name]; ok {
				return nil, fmt.Errorf("duplicate public model name")
			}
			routes[m.Name] = Route{name, m.Model, m.Capabilities.Snapshot()}
		}
	}
	return routes, nil
}

func (r Router) Resolve(model string) (Route, error) {
	route, ok := r[model]
	if !ok {
		return Route{}, fmt.Errorf("model is not configured")
	}
	route.Capabilities = route.Capabilities.Snapshot()
	return route, nil
}
