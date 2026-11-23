package gateway

import "fmt"

type Model struct{ Name, Model string }
type Upstream struct {
	Protocol, BaseURL, APIKey, ProxyURL, AnthropicVersion string
	Models                                                []Model
}
type Route struct{ Upstream, Model string }
type Router map[string]Route

func newRouter(upstreams map[string]Upstream) (Router, error) {
	routes := Router{}
	for name, u := range upstreams {
		for _, m := range u.Models {
			if _, ok := routes[m.Name]; ok {
				return nil, fmt.Errorf("duplicate public model name")
			}
			routes[m.Name] = Route{name, m.Model}
		}
	}
	return routes, nil
}

func (r Router) Resolve(model string) (Route, error) {
	route, ok := r[model]
	if !ok {
		return Route{}, fmt.Errorf("model is not configured")
	}
	return route, nil
}
