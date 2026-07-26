package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const validApplicationStory = `
app:
  id: pog
  version: 1.0.0
root: ready
world:
  change_summary: {type: object, default: {}}
intents:
  open_change:
    title: Open change
    slots:
      change_id: {type: string, required: true}
states:
  ready:
    on:
      open_change: [{target: ready}]
application:
  schema: application/v1
  name: POG
  description: Shape and deliver product changes.
  semantic_ref: pog.application
  data:
    change_summary:
      source: world.change_summary
      sensitivity: public
      policy: include
  feedback:
    context:
      workflow_state:
        source: frame.workflow.state
        sensitivity: internal
        policy: include
  shell:
    contract: "@kitsoki/wizard-ui/v1"
    presentation: default
    entry: dashboard
  navigation:
    - id: dashboard
      name: Dashboard
      description: Open the portfolio overview.
      semantic_ref: pog.nav.dashboard
      page: dashboard
  pages:
    dashboard:
      name: Portfolio
      description: Summarize the active portfolio.
      semantic_ref: pog.page.dashboard
      regions:
        main:
          name: Portfolio overview
          description: Show active changes and actions.
          semantic_ref: pog.region.dashboard-main
          items:
            - card:
                id: active-changes
                name: Active changes
                description: List changes currently being delivered.
                semantic_ref: pog.card.active-changes
                component: pog.change-list
                props:
                  source: "{{ world.change_summary }}"
                actions: [pog.change.open]
  components:
    pog.change-list:
      name: Change list
      description: Present change summaries.
      semantic_ref: pog.component.change-list
      web:
        module: ui/web/ChangeList.vue
        export: default
      props_schema: schemas/change-list-props.json
      fallback:
        element: list
  actions:
    pog.change.open:
      name: Open change
      description: Open a selected change.
      semantic_ref: pog.action.change-open
      handler: pog.change.open
  surfaces:
    web:
      presentation: custom
      entry: ui/web/main.ts
    vscode:
      reuse: web
      native:
        commands: [pog.change.open]
    tui:
      projection: cards
exports:
  handlers:
    pog.change.open:
      name: Open change
      description: Open a change and return its application frame.
      semantic_ref: pog.handler.change-open
      input_schema: schemas/change-open.json
      output_schema: schemas/application-frame.json
      session: required
      effect: read
      routing_mode: exact
      outcomes: [ok, not_found, forbidden]
      dispatch:
        intent: open_change
        state: ready
        slots_from: input
      expose: [jsonrpc, mcp, cli]
events:
  change-updated:
    source: pog.change.updated
    input_schema: schemas/change-updated.json
    session: required
    mode: background
    dispatch:
      handler: pog.change.open
`

func TestLoadBytesApplicationContract(t *testing.T) {
	def, err := LoadBytes([]byte(validApplicationStory))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Application == nil || def.Application.Schema != ApplicationSchemaV1 {
		t.Fatalf("application = %#v", def.Application)
	}
	if got := def.Application.Pages["dashboard"].Regions["main"].Items[0].Card.Component; got != "pog.change-list" {
		t.Fatalf("card component = %q", got)
	}
	if def.Exports == nil || def.Exports.Handlers["pog.change.open"] == nil {
		t.Fatalf("handlers = %#v", def.Exports)
	}
	if got := def.Events["change-updated"].Dispatch.Handler; got != "pog.change.open" {
		t.Fatalf("event handler = %q", got)
	}
	wire, err := json.Marshal(def.Application)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"semantic_ref"`, `"props_schema"`} {
		if !strings.Contains(string(wire), key) {
			t.Errorf("application JSON missing %s: %s", key, wire)
		}
	}
	handlerWire, err := json.Marshal(def.Exports.Handlers["pog.change.open"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(handlerWire), `"routing_mode"`) {
		t.Errorf("handler JSON missing routing_mode: %s", handlerWire)
	}
}

func TestApplicationDataValidation(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{
			name:    "name",
			replace: "    change_summary:\n",
			with:    "    Change.Summary:\n",
			want:    "must start with a lowercase letter",
		},
		{
			name:    "source",
			replace: "source: world.change_summary",
			with:    "source: world.missing",
			want:    `names undeclared world key "missing"`,
		},
		{
			name:    "ambient source",
			replace: "source: world.change_summary",
			with:    "source: env.HOME",
			want:    "must name one declared world key",
		},
		{
			name:    "secret include",
			replace: "source: world.change_summary\n      sensitivity: public",
			with:    "source: world.change_summary\n      sensitivity: secret",
			want:    "sensitive or secret data may not use policy include",
		},
		{
			name:    "unknown page scope",
			replace: "      policy: include\n",
			with:    "      policy: include\n      pages: [missing]\n",
			want:    `"missing" does not name an application page`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(validApplicationStory, tt.replace, tt.with, 1)
			if input == validApplicationStory {
				t.Fatalf("test replacement %q not found", tt.replace)
			}
			_, err := LoadBytes([]byte(input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestApplicationContractRejectsTypedBindingsOnUnverifiedLocalComponent(t *testing.T) {
	def := &AppDef{
		App: AppMeta{ID: "demo", Version: "1.0.0"},
		Application: &ApplicationContract{
			Schema: ApplicationSchemaV1, Name: "Demo", Description: "Exercise bindings",
			SemanticRef: "demo.application", Shell: ApplicationShell{Entry: "home"},
			Pages: map[string]*ApplicationPage{
				"home": {
					Name: "Home", Description: "Show the component", SemanticRef: "demo.page.home",
					Regions: map[string]*ApplicationRegion{
						"main": {
							Name: "Main", Description: "Show content", SemanticRef: "demo.region.main",
							Items: []ApplicationRegionItem{{Card: &ApplicationCard{
								ID: "card", Name: "Card", Description: "Show local component",
								SemanticRef: "demo.card.local", Component: "demo.local",
								Bindings: &ApplicationComponentBindings{
									Props: map[string]*ApplicationValueBinding{
										"title": {
											Source: "literal", Value: "Unsafe", ValueSet: true,
										},
									},
								},
							}}},
						},
					},
				},
			},
			Components: map[string]*ApplicationComponent{
				"demo.local": {
					Name: "Local", Description: "A local component",
					SemanticRef: "demo.component.local", PropsSchema: "props.json",
					Web: &ApplicationWebComponent{Module: "ui/local.js"},
				},
			},
		},
	}
	errs := validateApplicationContract(def, "app.yaml")
	if got := fmt.Sprint(errs); !strings.Contains(got, "requires a component selected from a lock-verified application package") {
		t.Fatalf("validation errors = %v", errs)
	}
}

func TestApplicationContractRequiresAPage(t *testing.T) {
	def := &AppDef{
		App: AppMeta{ID: "pog"},
		Application: &ApplicationContract{
			Schema: ApplicationSchemaV1, Name: "POG",
			Description: "Shape changes.", SemanticRef: "pog.application",
		},
	}
	errs := validateApplicationContract(def, "app.yaml")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "application.pages: must declare at least one page") {
		t.Fatalf("missing pages errors = %v", errs)
	}
}

func TestLoadBytesLegacyStoryDoesNotRequireApplication(t *testing.T) {
	_, err := LoadBytes([]byte(`
app: {id: legacy, version: 1.0.0}
root: ready
states:
  ready: {terminal: true}
`))
	if err != nil {
		t.Fatalf("legacy story: %v", err)
	}
}

func TestApplicationContractValidation(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{
			name:    "schema",
			replace: "schema: application/v1",
			with:    "schema: application/v2",
			want:    `application.schema: must be "application/v1"`,
		},
		{
			name:    "qualified semantic ref",
			replace: "semantic_ref: pog.page.dashboard",
			with:    "semantic_ref: other.page.dashboard",
			want:    `must be application-qualified with "pog."`,
		},
		{
			name:    "duplicate semantic ref",
			replace: "semantic_ref: pog.component.change-list",
			with:    "semantic_ref: pog.page.dashboard",
			want:    `duplicates application.pages.dashboard`,
		},
		{
			name:    "navigation page",
			replace: "page: dashboard",
			with:    "page: missing",
			want:    `"missing" does not name an application page`,
		},
		{
			name:    "card component",
			replace: "component: pog.change-list",
			with:    "component: pog.missing",
			want:    `"pog.missing" is not declared in application.components`,
		},
		{
			name:    "card action",
			replace: "actions: [pog.change.open]",
			with:    "actions: [pog.change.missing]",
			want:    `"pog.change.missing" is not declared in application.actions`,
		},
		{
			name:    "action handler",
			replace: "handler: pog.change.open",
			with:    "handler: pog.change.missing",
			want:    `"pog.change.missing" is not declared in exports.handlers`,
		},
		{
			name:    "fallback",
			replace: "      fallback:\n        element: list\n",
			with:    "",
			want:    "fallback: is required because the custom web component is used on a non-web surface",
		},
		{
			name:    "event mode",
			replace: "mode: background",
			with:    "mode: deferred",
			want:    `"deferred" is not one of background|interrupt`,
		},
		{
			name:    "routing mode",
			replace: "routing_mode: exact",
			with:    "routing_mode: guess",
			want:    `"guess" is not one of exact|synonym|semantic|llm|off`,
		},
		{
			name:    "sensitive feedback",
			replace: "sensitivity: internal",
			with:    "sensitivity: secret",
			want:    "sensitive or secret context may not use policy include",
		},
		{
			name:    "handler intent",
			replace: "intent: open_change",
			with:    "intent: missing_intent",
			want:    `"missing_intent" does not resolve`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(validApplicationStory, tt.replace, tt.with, 1)
			if input == validApplicationStory {
				t.Fatalf("test replacement %q not found", tt.replace)
			}
			_, err := LoadBytes([]byte(input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestHandlerEffectPolicyValidation(t *testing.T) {
	writeWithoutIdempotency := strings.Replace(validApplicationStory, "effect: read", "effect: write", 1)
	if _, err := LoadBytes([]byte(writeWithoutIdempotency)); err == nil ||
		!strings.Contains(err.Error(), "idempotency: is required for write and external handlers") {
		t.Fatalf("write handler error = %v", err)
	}

	retryableExternal := strings.Replace(validApplicationStory, "effect: read", "effect: external", 1)
	retryableExternal = strings.Replace(retryableExternal,
		"      routing_mode: exact",
		"      routing_mode: exact\n      idempotency: {key: input.change_id, scope: application}\n      retry: {max_attempts: 3}",
		1)
	if _, err := LoadBytes([]byte(retryableExternal)); err == nil ||
		!strings.Contains(err.Error(), "retryable external handler must declare exactly one of compensation or compensation_impossible") {
		t.Fatalf("retryable external error = %v", err)
	}

	withJustification := strings.Replace(retryableExternal,
		"      retry: {max_attempts: 3}",
		"      retry: {max_attempts: 3}\n      compensation_impossible: The upstream API has no inverse.",
		1)
	if _, err := LoadBytes([]byte(withJustification)); err != nil {
		t.Fatalf("external handler with explicit policy: %v", err)
	}
}

func TestMergeApplicationDeclarations(t *testing.T) {
	dst := &AppDef{
		Application: &ApplicationContract{
			Schema: ApplicationSchemaV1,
			Name:   "POG",
			Pages: map[string]*ApplicationPage{
				"dashboard": {Name: "Dashboard"},
			},
		},
		Exports: &ExportsBlock{
			Intents: []string{"open"},
			Handlers: map[string]*ApplicationHandler{
				"pog.open": {Name: "Open"},
			},
		},
	}
	src := &AppDef{
		Application: &ApplicationContract{
			Description: "Shape changes.",
			SemanticRef: "pog.application",
			Components: map[string]*ApplicationComponent{
				"pog.list": {Name: "List"},
			},
		},
		Exports: &ExportsBlock{
			Intents: []string{"search"},
			Handlers: map[string]*ApplicationHandler{
				"pog.search": {Name: "Search"},
			},
		},
		Events: map[string]*ApplicationEvent{"updated": {Source: "pog.updated"}},
	}
	var errs []string
	mergeApplicationDeclarations(dst, src, func(message string) { errs = append(errs, message) })
	if len(errs) != 0 {
		t.Fatalf("merge errors = %v", errs)
	}
	if dst.Application.Description != "Shape changes." ||
		dst.Application.Components["pog.list"] == nil ||
		dst.Exports.Handlers["pog.search"] == nil ||
		dst.Events["updated"] == nil {
		t.Fatalf("merged definition = %#v", dst)
	}
	if got := dst.Exports.Intents; len(got) != 2 || got[0] != "open" || got[1] != "search" {
		t.Fatalf("exported intents = %v", got)
	}

	mergeApplicationDeclarations(dst, src, func(message string) { errs = append(errs, message) })
	if !containsExact(errs, `include: application.description is already declared`) ||
		!containsExact(errs, `include: application component "pog.list" is already declared`) ||
		!containsExact(errs, `include: exported handler "pog.search" is already declared`) {
		t.Fatalf("collision errors = %v", errs)
	}
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
