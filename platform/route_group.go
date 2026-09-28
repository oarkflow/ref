package platform

import "fmt"

// expandRouteGroups turns every RouteGroupSpec into ordinary RouteSpecs
// appended to doc.Routes, then clears doc.RouteGroups. It runs before
// validateDocument, so a group is invisible to everything downstream —
// the planner, the compiler, the mounter — and a misconfigured group
// produces exactly the same error a hand-written route would.
//
// Path joining: Prefix + route.Path, with exactly one "/" between them
// regardless of whether either side already has one, so "/api/v1" + "/x"
// and "/api/v1/" + "x" both produce "/api/v1/x".
func expandRouteGroups(doc Document) (Document, error) {
	if len(doc.RouteGroups) == 0 {
		return doc, nil
	}
	names := map[string]bool{}
	for _, r := range doc.Routes {
		names[r.Name] = true
	}
	routes := make([]RouteSpec, 0, len(doc.Routes)+len(doc.RouteGroups)*2)
	routes = append(routes, doc.Routes...)

	for _, group := range doc.RouteGroups {
		if len(group.Routes) == 0 {
			return doc, fmt.Errorf("ref/platform: route_group %q declares no routes", group.Name)
		}
		for _, route := range group.Routes {
			if route.Name == "" {
				return doc, fmt.Errorf("ref/platform: route_group %q has a route with no name", group.Name)
			}
			if names[route.Name] {
				return doc, fmt.Errorf("ref/platform: route_group %q: duplicate route %q", group.Name, route.Name)
			}
			names[route.Name] = true

			route.Path = joinRoutePath(group.Prefix, route.Path)
			if route.Session == "" {
				route.Session = group.Session
			}
			if route.Auth == "" {
				route.Auth = group.Auth
			}
			if route.Authz == nil {
				route.Authz = group.Authz
			}
			if route.RateLimit == nil {
				route.RateLimit = group.RateLimit
			}
			if route.CacheControl == "" {
				route.CacheControl = group.CacheControl
			}
			if len(group.Tags) > 0 {
				route.Tags = append(append([]string{}, group.Tags...), route.Tags...)
			}
			routes = append(routes, route)
		}
	}

	doc.Routes = routes
	doc.RouteGroups = nil
	return doc, nil
}

func joinRoutePath(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	prefixHasSlash := prefix[len(prefix)-1] == '/'
	pathHasSlash := path[0] == '/'
	switch {
	case prefixHasSlash && pathHasSlash:
		return prefix + path[1:]
	case !prefixHasSlash && !pathHasSlash:
		return prefix + "/" + path
	default:
		return prefix + path
	}
}
