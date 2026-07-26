package app

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var applicationRouteParam = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var applicationRouteStatic = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

type ApplicationRouteTemplate struct {
	Template string
	Params   []string
	segments []applicationRouteSegment
}

type ApplicationRouteMatch struct {
	Page   string
	Route  ApplicationRouteTemplate
	Path   string
	Params map[string]any
}

type ApplicationPageBinding struct {
	Page  string
	Route string
	State string
}

type applicationRouteSegment struct {
	value string
	param bool
}

type applicationBindingIssue struct {
	Path    string
	Message string
}

// ApplicationPageBindings returns the effective route and story-state binding
// for every page. A navigation declaration may supply a binding omitted by its
// page, but all aliases targeting that page must agree on the same value.
func ApplicationPageBindings(contract *ApplicationContract) map[string]ApplicationPageBinding {
	if contract == nil {
		return map[string]ApplicationPageBinding{}
	}
	out := make(map[string]ApplicationPageBinding, len(contract.Pages))
	for pageID, page := range contract.Pages {
		if page == nil {
			continue
		}
		out[pageID] = ApplicationPageBinding{Page: pageID, Route: page.Route, State: canonicalStatePath(page.State)}
	}
	for _, nav := range contract.Navigation {
		binding, ok := out[nav.Page]
		if !ok {
			continue
		}
		if binding.Route == "" {
			binding.Route = nav.Route
		}
		if binding.State == "" {
			binding.State = canonicalStatePath(nav.State)
		}
		out[nav.Page] = binding
	}
	return out
}

func ApplicationPageBindingFor(contract *ApplicationContract, page string) (ApplicationPageBinding, bool) {
	binding, ok := ApplicationPageBindings(contract)[page]
	return binding, ok
}

func ApplicationPageForState(contract *ApplicationContract, state string) (string, bool) {
	pages := ApplicationPagesForState(contract, state)
	if len(pages) != 1 {
		return "", false
	}
	return pages[0], true
}

func ApplicationPagesForState(contract *ApplicationContract, state string) []string {
	state = canonicalStatePath(state)
	var pages []string
	for page, binding := range ApplicationPageBindings(contract) {
		if binding.State == state && state != "" {
			pages = append(pages, page)
		}
	}
	sort.Strings(pages)
	return pages
}

func ParseApplicationRouteTemplate(template string) (ApplicationRouteTemplate, error) {
	if template == "" {
		return ApplicationRouteTemplate{}, nil
	}
	if !strings.HasPrefix(template, "/") {
		return ApplicationRouteTemplate{}, fmt.Errorf("must be an absolute path")
	}
	if strings.ContainsAny(template, "?#") {
		return ApplicationRouteTemplate{}, fmt.Errorf("must not contain a query or fragment")
	}
	if template != "/" && strings.HasSuffix(template, "/") {
		return ApplicationRouteTemplate{}, fmt.Errorf("must not have a trailing slash")
	}
	if strings.Contains(template, "//") {
		return ApplicationRouteTemplate{}, fmt.Errorf("must not contain an empty path segment")
	}
	parsed := ApplicationRouteTemplate{Template: template}
	if template == "/" {
		return parsed, nil
	}
	seen := map[string]struct{}{}
	for _, raw := range strings.Split(strings.TrimPrefix(template, "/"), "/") {
		segment := applicationRouteSegment{value: raw}
		if strings.HasPrefix(raw, "{") || strings.HasSuffix(raw, "}") {
			if len(raw) < 3 || raw[0] != '{' || raw[len(raw)-1] != '}' ||
				strings.ContainsAny(raw[1:len(raw)-1], "{}") {
				return ApplicationRouteTemplate{}, fmt.Errorf("segment %q must be a whole {param} placeholder", raw)
			}
			name := raw[1 : len(raw)-1]
			if !applicationRouteParam.MatchString(name) {
				return ApplicationRouteTemplate{}, fmt.Errorf("parameter %q must match %s", name, applicationRouteParam)
			}
			if _, duplicate := seen[name]; duplicate {
				return ApplicationRouteTemplate{}, fmt.Errorf("parameter %q is duplicated", name)
			}
			seen[name] = struct{}{}
			segment.value = name
			segment.param = true
			parsed.Params = append(parsed.Params, name)
		} else if !applicationRouteStatic.MatchString(raw) || raw == "." || raw == ".." {
			return ApplicationRouteTemplate{}, fmt.Errorf("segment %q is not canonical", raw)
		}
		parsed.segments = append(parsed.segments, segment)
	}
	return parsed, nil
}

func ResolveApplicationRoute(contract *ApplicationContract, path string) (ApplicationRouteMatch, error) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return ApplicationRouteMatch{}, fmt.Errorf("application route %q must be an absolute path without query or fragment", path)
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		return ApplicationRouteMatch{}, fmt.Errorf("application route %q is not canonical", path)
	}
	bindings := ApplicationPageBindings(contract)
	pages := make([]string, 0, len(bindings))
	for page := range bindings {
		pages = append(pages, page)
	}
	sort.Strings(pages)
	for _, page := range pages {
		binding := bindings[page]
		if binding.Route == "" {
			continue
		}
		route, err := ParseApplicationRouteTemplate(binding.Route)
		if err != nil {
			return ApplicationRouteMatch{}, fmt.Errorf("application page %q route: %w", page, err)
		}
		params, ok, err := matchApplicationRoute(route, path)
		if err != nil {
			return ApplicationRouteMatch{}, err
		}
		if ok {
			canonical, formatErr := FormatApplicationRoute(route.Template, params)
			if formatErr != nil || canonical != path {
				return ApplicationRouteMatch{}, fmt.Errorf("application route %q is not canonical", path)
			}
			return ApplicationRouteMatch{Page: page, Route: route, Path: path, Params: params}, nil
		}
	}
	return ApplicationRouteMatch{}, fmt.Errorf("application route %q does not match a declared page", path)
}

func FormatApplicationRoute(template string, params map[string]any) (string, error) {
	route, err := ParseApplicationRouteTemplate(template)
	if err != nil {
		return "", err
	}
	if route.Template == "" {
		return "", nil
	}
	if route.Template == "/" {
		return "/", nil
	}
	parts := make([]string, 0, len(route.segments))
	for _, segment := range route.segments {
		if !segment.param {
			parts = append(parts, segment.value)
			continue
		}
		value, ok := params[segment.value]
		if !ok {
			return "", fmt.Errorf("route parameter %q is required", segment.value)
		}
		text, ok := value.(string)
		if !ok || text == "" {
			return "", fmt.Errorf("route parameter %q must be a non-empty string", segment.value)
		}
		parts = append(parts, url.PathEscape(text))
	}
	return "/" + strings.Join(parts, "/"), nil
}

func matchApplicationRoute(route ApplicationRouteTemplate, path string) (map[string]any, bool, error) {
	var parts []string
	if path != "/" {
		parts = strings.Split(strings.TrimPrefix(path, "/"), "/")
	}
	if len(parts) != len(route.segments) {
		return nil, false, nil
	}
	params := map[string]any{}
	for index, segment := range route.segments {
		part, err := url.PathUnescape(parts[index])
		if err != nil || part == "" || strings.Contains(part, "/") {
			return nil, false, fmt.Errorf("application route %q contains an invalid encoded segment", path)
		}
		if segment.param {
			params[segment.value] = part
		} else if part != segment.value {
			return nil, false, nil
		}
	}
	return params, true, nil
}

func validateApplicationPageBindings(def *AppDef, contract *ApplicationContract) []applicationBindingIssue {
	if contract == nil {
		return nil
	}
	var issues []applicationBindingIssue
	add := func(path, format string, args ...any) {
		issues = append(issues, applicationBindingIssue{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	bindings := ApplicationPageBindings(contract)
	for index, nav := range contract.Navigation {
		page := contract.Pages[nav.Page]
		if page == nil {
			continue
		}
		path := fmt.Sprintf("application.navigation[%d]", index)
		binding := bindings[nav.Page]
		if nav.Route != "" && nav.Route != binding.Route {
			add(path+".route", "%q disagrees with canonical page route %q", nav.Route, binding.Route)
		}
		if nav.State != "" && canonicalStatePath(nav.State) != binding.State {
			add(path+".state", "%q disagrees with canonical page state %q", nav.State, binding.State)
		}
	}

	type compiledBinding struct {
		page  string
		path  string
		route ApplicationRouteTemplate
	}
	var routes []compiledBinding
	for _, pageID := range sortedKeys(contract.Pages) {
		binding := bindings[pageID]
		pagePath := "application.pages." + pageID
		if binding.Route != "" {
			route, err := ParseApplicationRouteTemplate(binding.Route)
			if err != nil {
				add(pagePath+".route", "%v", err)
			} else {
				routes = append(routes, compiledBinding{page: pageID, path: pagePath + ".route", route: route})
				decl := contract.Pages[pageID]
				for param, worldKey := range decl.RouteBindings {
					bindingPath := pagePath + ".route_bindings." + param
					if !containsApplicationRouteParam(route.Params, param) {
						add(bindingPath, "%q is not declared by route %q", param, binding.Route)
						continue
					}
					worldDef, ok := def.World[worldKey]
					if !ok {
						add(bindingPath, "world key %q is not declared", worldKey)
					} else if worldDef.Type != "string" {
						add(bindingPath, "world key %q must have type string (got %q)", worldKey, worldDef.Type)
					}
				}
			}
		} else if len(contract.Pages[pageID].RouteBindings) > 0 {
			add(pagePath+".route_bindings", "requires a canonical page route")
		}
		if binding.State != "" {
			if state, ok := def.LookupState(StatePath(binding.State)); !ok || state == nil {
				add(pagePath+".state", "%q does not name a story state", binding.State)
			}
		}
	}
	for i := range routes {
		for j := i + 1; j < len(routes); j++ {
			if applicationRoutesOverlap(routes[i].route, routes[j].route) {
				add(routes[j].path, "%q is ambiguous with page %q route %q",
					routes[j].route.Template, routes[i].page, routes[i].route.Template)
			}
		}
	}
	return issues
}

func containsApplicationRouteParam(params []string, name string) bool {
	for _, param := range params {
		if param == name {
			return true
		}
	}
	return false
}

func applicationRoutesOverlap(left, right ApplicationRouteTemplate) bool {
	if len(left.segments) != len(right.segments) {
		return false
	}
	for i := range left.segments {
		l, r := left.segments[i], right.segments[i]
		if !l.param && !r.param && l.value != r.value {
			return false
		}
	}
	return true
}

func canonicalStatePath(state string) string {
	return strings.ReplaceAll(strings.TrimSpace(state), "/", ".")
}
