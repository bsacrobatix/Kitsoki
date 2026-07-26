package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/effect"
)

func TestFoldChildApplicationNamespacesFullContract(t *testing.T) {
	parent := &AppDef{
		App: AppMeta{ID: "parent"},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{}, Components: map[string]*ApplicationComponent{},
			Actions: map[string]*ApplicationAction{},
		},
		States: map[string]*State{
			"module": {States: map[string]*State{"entry": {Implements: []string{"reviewer"}}}},
		},
	}
	child := &AppDef{
		App:     AppMeta{ID: "child"},
		BaseDir: filepath.Join("fixtures", "child"),
		RoomInterfaces: map[string]*RoomInterfaceDef{
			"reviewer": {Intents: map[string]Intent{"go": {}}},
		},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{
				"home": {
					SemanticRef: "child.page.home",
					Regions: map[string]*ApplicationRegion{
						"main": {Items: []ApplicationRegionItem{{Card: &ApplicationCard{
							ID: "card", Component: "child.card", Actions: []string{"child.open"},
						}}}},
					},
				},
			},
			Components: map[string]*ApplicationComponent{
				"child.card": {
					SemanticRef: "child.component.card", PropsSchema: "schemas/card.json",
					Web: &ApplicationWebComponent{Module: "ui/card.js"},
				},
			},
			Actions: map[string]*ApplicationAction{
				"child.open": {
					SemanticRef: "child.action.open", Handler: "child.open",
					InputSchema: "schemas/open.json", Intent: "go", State: "entry",
					RoomInterface: "reviewer",
				},
			},
		},
		Exports: &ExportsBlock{
			Intents: []string{"go"},
			Application: &ApplicationExports{
				Pages: []string{"home"}, Components: []string{"child.card"},
				Actions: []string{"child.open"},
			},
			Handlers: map[string]*ApplicationHandler{
				"child.open": {
					SemanticRef: "child.handler.open", InputSchema: "schemas/open.json",
					OutputSchema: "schemas/result.json", Effect: effect.Read,
					Dispatch: &ApplicationHandlerDispatch{Intent: "go", State: "entry", RoomInterface: "reviewer"},
				},
			},
		},
	}
	rw := &childRewriter{alias: "module", childIntent: map[string]struct{}{"go": {}}}
	if errs := foldChildApplication(parent, child, "module", rw, "app.yaml"); len(errs) != 0 {
		t.Fatalf("foldChildApplication: %v", errs)
	}
	componentID := "parent.module.card"
	actionID := "parent.module.open"
	page := parent.Application.Pages["module__home"]
	card := page.Regions["main"].Items[0].Card
	if card.Component != componentID || card.Actions[0] != actionID {
		t.Fatalf("composed card = %#v", card)
	}
	action := parent.Application.Actions[actionID]
	if action.Handler != actionID || action.Intent != "module__go" ||
		action.State != "module.entry" || action.RoomInterface != "module__reviewer" {
		t.Fatalf("composed action = %#v", action)
	}
	if !strings.HasSuffix(action.InputSchema, filepath.Join("fixtures", "child", "schemas", "open.json")) {
		t.Fatalf("action input schema = %q", action.InputSchema)
	}
	if _, ok := parent.ImportedApplicationOwners["child"]; !ok {
		t.Fatal("child semantic owner was not retained")
	}
	if got := parent.States["module"].States["entry"].Implements[0]; got != "module__reviewer" {
		t.Fatalf("implementation ref = %q", got)
	}
}

func TestApplicationOverrideRequiresCompatibilityAndSemanticAlias(t *testing.T) {
	base := &ApplicationComponent{
		SemanticRef: "child.component.card", PropsSchema: "schemas/card.json",
		Fallback: &ApplicationComponentFallback{Element: "prose"},
	}
	compatible := &ApplicationComponent{
		SemanticRef: "parent.component.card", SemanticAliases: []string{"child.component.card"},
		PropsSchema: "schemas/card.json", Fallback: &ApplicationComponentFallback{Element: "prose"},
	}
	if err := compatibleComponentOverride(base, compatible); err != nil {
		t.Fatalf("compatible override: %v", err)
	}
	incompatible := *compatible
	incompatible.PropsSchema = "schemas/other.json"
	if err := compatibleComponentOverride(base, &incompatible); err == nil ||
		!strings.Contains(err.Error(), "props_schema") {
		t.Fatalf("incompatible override error = %v", err)
	}
}

func TestLoadComposesImportedApplicationContract(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `
app: {id: child, version: 1.0.0}
root: idle
intents:
  go: {title: Go, description: Continue the child flow.}
states:
  idle:
    description: Child room
    on:
      go: [{target: done}]
  done: {terminal: true}
exports:
  intents: [go]
  application:
    pages: [home]
    actions: [child.go]
application:
  schema: application/v1
  name: Child
  description: Child application fragment.
  semantic_ref: child.application
  pages:
    home:
      name: Child home
      description: Show the child workflow.
      semantic_ref: child.page.home
      regions:
        main:
          name: Main
          description: Show child content.
          semantic_ref: child.region.main
          items:
            - card:
                id: task
                name: Task
                description: Show the current task.
                semantic_ref: child.card.task
                component: prose
                actions: [child.go]
  actions:
    child.go:
      name: Go
      description: Continue the child flow.
      semantic_ref: child.action.go
      intent: go
      state: idle
`)
	parentDir := mkdirT(t, root, "parent")
	mustWrite(t, parentDir, "app.yaml", `
app: {id: parent, version: 1.0.0}
root: start
imports:
  module:
    source: ../child
    entry: idle
states:
  start: {terminal: true}
application:
  schema: application/v1
  name: Parent
  description: Parent application.
  semantic_ref: parent.application
  pages:
    home:
      name: Parent home
      description: Show the parent workflow.
      semantic_ref: parent.page.home
      regions:
        main:
          name: Main
          description: Show parent content.
          semantic_ref: parent.region.main
          items:
            - card:
                id: welcome
                name: Welcome
                description: Show a welcome message.
                semantic_ref: parent.card.welcome
                component: prose
`)
	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.Application.Pages["module__home"] == nil || def.Application.Actions["parent.module.go"] == nil {
		t.Fatalf("composed application = %#v", def.Application)
	}
	action := def.Application.Actions["parent.module.go"]
	if action.State != "module.idle" || action.Intent != "module__go" {
		t.Fatalf("composed action = %#v", action)
	}
	if page := def.Application.Pages["module__home"]; page.Origin.Story != "child" ||
		page.Origin.Member != "application.pages.home" ||
		action.Origin.Story != "child" {
		t.Fatalf("composed provenance page=%#v action=%#v", page.Origin, action.Origin)
	}
}

func TestLoadKeepsUnexportedApplicationMembersPrivate(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `
app: {id: child, version: 1.0.0}
root: idle
states:
  idle: {terminal: true}
application:
  schema: application/v1
  name: Child
  description: Private child application.
  semantic_ref: child.application
  pages:
    private:
      name: Private
      description: Remain private to the child.
      semantic_ref: child.page.private
`)
	parentDir := mkdirT(t, root, "parent")
	mustWrite(t, parentDir, "app.yaml", `
app: {id: parent, version: 1.0.0}
root: start
imports:
  module: {source: ../child, entry: idle}
states:
  start: {terminal: true}
application:
  schema: application/v1
  name: Parent
  description: Parent application.
  semantic_ref: parent.application
  pages:
    home:
      name: Home
      description: Show the parent application.
      semantic_ref: parent.page.home
`)
	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, exposed := def.Application.Pages["module__private"]; exposed {
		t.Fatal("unexported child page became parent-visible")
	}
}

func TestFoldRejectsExportedActionTargetingPrivateIntent(t *testing.T) {
	parent := &AppDef{
		App: AppMeta{ID: "parent"},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{}, Components: map[string]*ApplicationComponent{},
			Actions: map[string]*ApplicationAction{},
		},
	}
	child := &AppDef{
		App: AppMeta{ID: "child"},
		Application: &ApplicationContract{Actions: map[string]*ApplicationAction{
			"child.go": {Intent: "go"},
		}},
		Exports: &ExportsBlock{Application: &ApplicationExports{Actions: []string{"child.go"}}},
	}
	errs := foldChildApplication(parent, child, "module", &childRewriter{
		alias: "module", childIntent: map[string]struct{}{"go": {}},
	}, "app.yaml")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), `targets private intent "go"`) {
		t.Fatalf("errors = %v", errs)
	}
}

func TestFoldRejectsExportedPagePrivateDependencies(t *testing.T) {
	parent := &AppDef{
		App: AppMeta{ID: "parent"},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{}, Components: map[string]*ApplicationComponent{},
			Actions: map[string]*ApplicationAction{},
		},
	}
	child := &AppDef{
		App: AppMeta{ID: "child"},
		Application: &ApplicationContract{
			Pages: map[string]*ApplicationPage{"home": {
				Regions: map[string]*ApplicationRegion{"main": {
					Items: []ApplicationRegionItem{{Card: &ApplicationCard{
						Component: "child.private", Actions: []string{"child.private"},
						Elements: []ViewElement{{
							Kind: "choice", ChoiceMode: "form", ChoiceIntent: "private",
						}},
					}}},
				}},
			}},
			Components: map[string]*ApplicationComponent{"child.private": {}},
			Actions:    map[string]*ApplicationAction{"child.private": {}},
		},
		Intents: map[string]Intent{"private": {}},
		Exports: &ExportsBlock{Application: &ApplicationExports{Pages: []string{"home"}}},
	}
	errs := foldChildApplication(parent, child, "module", &childRewriter{alias: "module"}, "app.yaml")
	joined := fmt.Sprint(errs)
	if !strings.Contains(joined, `private component "child.private"`) ||
		!strings.Contains(joined, `private action "child.private"`) ||
		!strings.Contains(joined, `private intent "private" from a typed element`) {
		t.Fatalf("errors = %v", errs)
	}
}

func TestMergeApplicationContractKeepsPackagesSchemasAndTokens(t *testing.T) {
	dst := &ApplicationContract{}
	src := &ApplicationContract{
		Packages: []ApplicationPackageUse{{Package: "kitsoki.wizard", Select: []string{"components.form"}}},
		Schemas:  map[string]string{"form": "schemas/form.json"},
		Tokens:   map[string]string{"default": "tokens/default.json"},
	}
	var errs []string
	mergeApplicationContract(dst, src, func(message string) { errs = append(errs, message) })
	if len(errs) != 0 || len(dst.Packages) != 1 ||
		dst.Schemas["form"] != "schemas/form.json" ||
		dst.Tokens["default"] != "tokens/default.json" {
		t.Fatalf("merged=%#v errors=%v", dst, errs)
	}
}

func TestApplicationOverridePathsRemainParentOwned(t *testing.T) {
	child := &AppDef{
		Application: &ApplicationContract{Components: map[string]*ApplicationComponent{
			"child.card": {
				SemanticRef: "child.component.card", PropsSchema: "schemas/card.json",
				Web: &ApplicationWebComponent{Module: "ui/child-card.js"},
			},
		}},
	}
	replacement := &ApplicationComponent{
		SemanticRef: "parent.component.card", SemanticAliases: []string{"child.component.card"},
		PropsSchema: "schemas/card.json", Web: &ApplicationWebComponent{Module: "ui/parent-card.js"},
	}
	var errs []string
	parentRoot := filepath.Join("fixtures", "parent")
	applyApplicationOverrides(child, &ApplicationOverrides{
		Components: map[string]*ApplicationComponent{"child.card": replacement},
	}, "parent", parentRoot, func(message string) { errs = append(errs, message) })
	got := child.Application.Components["child.card"]
	if len(errs) != 0 ||
		got.Web.Module != filepath.Join(parentRoot, "ui", "parent-card.js") ||
		got.PropsSchema != filepath.Join(parentRoot, "schemas", "card.json") ||
		got.Origin.Story != "parent" ||
		got.Origin.Member != "overrides.application.components.child.card" {
		t.Fatalf("override=%#v errors=%v", got, errs)
	}
}
