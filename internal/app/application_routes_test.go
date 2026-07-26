package app

import (
	"strings"
	"testing"
)

func TestApplicationRouteTemplateResolveAndFormat(t *testing.T) {
	contract := &ApplicationContract{Pages: map[string]*ApplicationPage{
		"change": {Route: "/changes/{change_id}"},
	}}
	match, err := ResolveApplicationRoute(contract, "/changes/chg-42")
	if err != nil {
		t.Fatal(err)
	}
	if match.Page != "change" || match.Params["change_id"] != "chg-42" {
		t.Fatalf("match = %#v", match)
	}
	formatted, err := FormatApplicationRoute(match.Route.Template, match.Params)
	if err != nil || formatted != match.Path {
		t.Fatalf("FormatApplicationRoute() = %q, %v", formatted, err)
	}
	if _, err := ResolveApplicationRoute(contract, "/changes/chg%2f42"); err == nil {
		t.Fatal("encoded path separator was accepted")
	}
}

func TestApplicationPageBindingValidationRejectsAmbiguityButAllowsStateAliases(t *testing.T) {
	def := &AppDef{
		States: map[string]*State{"overview": {}},
		World:  map[string]VarDef{"selected": {Type: "object"}},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{
				"list": {Route: "/changes", State: "overview"},
				"detail": {
					Route: "/changes/{change_id}", State: "overview",
					RouteBindings: map[string]string{
						"change_id": "selected", "unknown": "selected",
					},
				},
				"new": {Route: "/changes/new", State: "overview"},
			},
			Navigation: []ApplicationNavigation{
				{ID: "list", Page: "list", State: "overview"},
				{ID: "detail", Page: "detail", Route: "/wrong/{change_id}"},
			},
		},
	}
	issues := validateApplicationPageBindings(def, def.Application)
	var messages []string
	for _, issue := range issues {
		messages = append(messages, issue.Path+": "+issue.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "disagrees with canonical page route") {
		t.Fatalf("issues = %s, want navigation disagreement", joined)
	}
	if !strings.Contains(joined, "ambiguous with page") {
		t.Fatalf("issues = %s, want route ambiguity", joined)
	}
	if !strings.Contains(joined, "must have type string") ||
		!strings.Contains(joined, "is not declared by route") {
		t.Fatalf("issues = %s, want finite route binding errors", joined)
	}
	if strings.Contains(joined, "already bound") {
		t.Fatalf("many pages sharing one state were rejected: %s", joined)
	}
}

func TestApplicationPageBindingsDeriveCanonicalNavigationDeclarations(t *testing.T) {
	contract := &ApplicationContract{
		Pages: map[string]*ApplicationPage{"review": {}},
		Navigation: []ApplicationNavigation{{
			ID: "review", Page: "review", Route: "/reviews/{review_id}", State: "review/ready",
		}},
	}
	binding := ApplicationPageBindings(contract)["review"]
	if binding.Route != "/reviews/{review_id}" || binding.State != "review.ready" {
		t.Fatalf("binding = %#v", binding)
	}
}
