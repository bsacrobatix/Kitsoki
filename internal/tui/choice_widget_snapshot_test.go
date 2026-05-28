package tui_test

// Snapshot tests for choice widget View() output. The intent is to lock
// down the CURRENT visual experience — every interactively-discovered
// UX rule (auto-paramMode for single-item-with-param, «...» placeholder
// brackets, live-red invalid-type rendering, all-readonly form hint,
// no letter-defocus footgun, Tab off-ramp etc.) shows up in some
// snapshot. A bug that quietly changes one of these surfaces as a
// golden diff next time the suite runs.
//
// Run `go test ./internal/tui/... -update -run TestChoiceWidgetSnapshot`
// to regenerate the goldens after an intentional change. Goldens live
// at internal/tui/testdata/choice_widget_golden/.

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/tui"
)

// snapshotCase drives a widget from a freshly Open()ed state through a
// sequence of key inputs, then captures View(width) for the golden.
// world feeds the expr environment so When-guarded fields / items can
// be exercised (nil → empty world, the default for most cases).
type snapshotCase struct {
	name    string
	element app.ViewElement
	width   int
	world   map[string]any
	keys    []tea.KeyMsg
	runes   []rune // typed AFTER keys, useful for filling buffers
}

func TestChoiceWidgetSnapshot(t *testing.T) {
	cases := []snapshotCase{
		// ── single mode ─────────────────────────────────────────────
		{
			name:  "single_basic_initial",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Choose a color",
				ChoiceItems: []app.ChoiceItem{
					{Label: "Red", Intent: "chose_color", Slots: map[string]any{"color": "red"}},
					{Label: "Green", Intent: "chose_color", Slots: map[string]any{"color": "green"}},
					{Label: "Blue", Intent: "chose_color", Slots: map[string]any{"color": "blue"}},
				},
			},
		},
		{
			name:  "single_basic_cursor_on_third",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Choose a color",
				ChoiceItems: []app.ChoiceItem{
					{Label: "Red", Intent: "chose_color"},
					{Label: "Green", Intent: "chose_color"},
					{Label: "Blue", Intent: "chose_color"},
				},
			},
			keys: []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyDown}},
		},
		{
			name:  "single_with_hints",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick a profession",
				ChoiceItems: []app.ChoiceItem{
					{Label: "Banker", Hint: "$1,600 starting cash", Intent: "pick_profession"},
					{Label: "Carpenter", Hint: "$800", Intent: "pick_profession"},
					{Label: "Farmer", Hint: "$400 — highest score multiplier", Intent: "pick_profession"},
				},
			},
		},

		// ── single + param (the auto-paramMode case) ────────────────
		{
			name:  "single_one_item_param_string_initial",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Name your hero",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Name the hero",
						Hint:   "type a name, then Enter",
						Intent: "set_hero_name",
						Param: &app.ChoiceParam{
							Slot:        "name",
							Type:        "string",
							Placeholder: "e.g. Aria",
							Required:    true,
						},
					},
				},
			},
		},
		{
			name:  "single_one_item_param_string_typed",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Name your hero",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Name the hero",
						Intent: "set_hero_name",
						Param: &app.ChoiceParam{
							Slot:        "name",
							Type:        "string",
							Placeholder: "e.g. Aria",
						},
					},
				},
			},
			runes: []rune{'A', 'r', 'i', 'a'},
		},
		{
			name:  "single_one_item_param_int_invalid",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Set hero age",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Set age",
						Intent: "set_age",
						Param: &app.ChoiceParam{
							Slot:        "age",
							Type:        "int",
							Placeholder: "e.g. 27",
						},
					},
				},
			},
			runes: []rune{'a', 'b', 'c'},
		},
		{
			name:  "single_one_item_param_enum_initial",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick a class",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Set class",
						Intent: "set_class",
						Param: &app.ChoiceParam{
							Slot:   "class",
							Type:   "enum",
							Values: []string{"warrior", "mage", "rogue"},
						},
					},
				},
			},
		},
		{
			name:  "single_one_item_param_enum_cycled_twice",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick a class",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Set class",
						Intent: "set_class",
						Param: &app.ChoiceParam{
							Slot:   "class",
							Type:   "enum",
							Values: []string{"warrior", "mage", "rogue"},
						},
					},
				},
			},
			keys: []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeySpace}},
		},

		// ── multi mode ──────────────────────────────────────────────
		{
			name:  "multi_basic_initial",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "multi",
				ChoicePrompt: "Pick traits",
				ChoiceIntent: "chose_traits",
				ChoiceSlot:   "traits",
				ChoiceMin:    1,
				ChoiceMax:    3,
				ChoiceMinSet: true,
				ChoiceMaxSet: true,
				ChoiceItems: []app.ChoiceItem{
					{Value: "brave", Label: "Brave", Hint: "ignores fear"},
					{Value: "kind", Label: "Kind", Hint: "heals on rest"},
					{Value: "clever", Label: "Clever", Hint: "+10% xp"},
				},
			},
		},
		{
			name:  "multi_two_selected",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "multi",
				ChoicePrompt: "Pick traits",
				ChoiceIntent: "chose_traits",
				ChoiceSlot:   "traits",
				ChoiceMinSet: true,
				ChoiceMaxSet: true,
				ChoiceMin:    1,
				ChoiceMax:    3,
				ChoiceItems: []app.ChoiceItem{
					{Value: "brave", Label: "Brave"},
					{Value: "kind", Label: "Kind"},
					{Value: "clever", Label: "Clever"},
				},
			},
			keys: []tea.KeyMsg{
				{Type: tea.KeySpace},                     // toggle Brave
				{Type: tea.KeyDown}, {Type: tea.KeyDown}, // move to Clever
				{Type: tea.KeySpace}, // toggle Clever
			},
		},
		{
			name:  "multi_min_violation",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "multi",
				ChoicePrompt: "Pick traits",
				ChoiceIntent: "chose_traits",
				ChoiceSlot:   "traits",
				ChoiceMinSet: true,
				ChoiceMin:    1,
				ChoiceItems: []app.ChoiceItem{
					{Value: "brave", Label: "Brave"},
					{Value: "kind", Label: "Kind"},
				},
			},
			keys: []tea.KeyMsg{{Type: tea.KeyEnter}}, // submit with 0 picked → errMsg
		},

		// ── form mode ───────────────────────────────────────────────
		{
			name:  "form_string_int_initial",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Compose your hero",
				ChoiceIntent:   "submit_hero",
				ChoiceTemplate: "Hero {name} count {count}.",
				ChoiceFields: []app.ChoiceField{
					{Name: "name", Type: "string", Placeholder: "type a name", Required: true},
					{Name: "count", Type: "int", Default: 1},
				},
			},
		},
		{
			name:  "form_int_invalid",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Compose your hero",
				ChoiceIntent:   "submit_hero",
				ChoiceTemplate: "Hero {name} count {count}.",
				ChoiceFields: []app.ChoiceField{
					{Name: "name", Type: "string"},
					{Name: "count", Type: "int"},
				},
			},
			keys:  []tea.KeyMsg{{Type: tea.KeyTab}}, // cursor to count
			runes: []rune{'a', 'b', 'c'},
		},
		{
			name:  "form_int_valid",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Compose your hero",
				ChoiceIntent:   "submit_hero",
				ChoiceTemplate: "Hero {name} count {count}.",
				ChoiceFields: []app.ChoiceField{
					{Name: "name", Type: "string"},
					{Name: "count", Type: "int"},
				},
			},
			keys:  []tea.KeyMsg{{Type: tea.KeyTab}},
			runes: []rune{'4', '2'},
		},
		{
			name:  "form_bool_initial",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Toggle flag",
				ChoiceIntent:   "submit_toggle",
				ChoiceTemplate: "Active: {active}",
				ChoiceFields: []app.ChoiceField{
					{Name: "active", Type: "bool"},
				},
			},
		},
		{
			name:  "form_bool_toggled_on",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Toggle flag",
				ChoiceIntent:   "submit_toggle",
				ChoiceTemplate: "Active: {active}",
				ChoiceFields: []app.ChoiceField{
					{Name: "active", Type: "bool"},
				},
			},
			keys: []tea.KeyMsg{{Type: tea.KeySpace}},
		},
		{
			name:  "form_enum_initial",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Pick method",
				ChoiceIntent:   "submit_method",
				ChoiceTemplate: "Method: {method}",
				ChoiceFields: []app.ChoiceField{
					{Name: "method", Type: "enum", Values: []string{"ford", "caulk", "ferry"}, Default: "ford"},
				},
			},
		},
		{
			name:  "form_enum_cycled_twice",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Pick method",
				ChoiceIntent:   "submit_method",
				ChoiceTemplate: "Method: {method}",
				ChoiceFields: []app.ChoiceField{
					{Name: "method", Type: "enum", Values: []string{"ford", "caulk", "ferry"}, Default: "ford"},
				},
			},
			keys: []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeySpace}},
		},
		{
			name:  "form_placeholder_empty",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Compose",
				ChoiceIntent:   "submit",
				ChoiceTemplate: "{name}",
				ChoiceFields: []app.ChoiceField{
					{Name: "name", Type: "string", Placeholder: "type a name"},
				},
			},
		},
		{
			name:  "form_all_readonly",
			width: 80,
			element: app.ViewElement{
				Kind:           "choice",
				ChoiceMode:     "form",
				ChoicePrompt:   "Submit your total",
				ChoiceIntent:   "submit_total",
				ChoiceTemplate: "Total {total}",
				ChoiceFields: []app.ChoiceField{
					{Name: "total", Type: "int", Readonly: true, Expr: "100 + 30"},
				},
			},
		},

		// ── footer + width variants ─────────────────────────────────
		{
			name:  "single_narrow_60",
			width: 60,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick one",
				ChoiceItems: []app.ChoiceItem{
					{Label: "Option one is the first choice", Hint: "first", Intent: "go"},
					{Label: "Option two", Hint: "second", Intent: "go"},
				},
			},
		},

		// ── coverage gap fills (P2-5) ────────────────────────────────
		// Single-mode paramMode entered via an explicit Enter (NOT the
		// auto-paramMode-on-Open ergonomic shortcut). With two items
		// where the second has a param, navigating to it and pressing
		// Enter must drop into the inline param prompt under that row.
		{
			name:  "single_basic_param_entered_via_enter",
			width: 80,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick action",
				ChoiceItems: []app.ChoiceItem{
					{
						Label:  "Stand pat",
						Hint:   "no slot",
						Intent: "stand_pat",
					},
					{
						Label:  "Name a target",
						Hint:   "type a name",
						Intent: "name_target",
						Param: &app.ChoiceParam{
							Slot:        "target",
							Type:        "string",
							Placeholder: "e.g. dragon",
						},
					},
				},
			},
			keys: []tea.KeyMsg{
				{Type: tea.KeyDown},  // move cursor to second item
				{Type: tea.KeyEnter}, // explicit Enter → paramMode
			},
		},

		// Form-mode per-field `when:` toggling. The hidden field's When
		// guard reads world.show — flipping it in the test world makes
		// the field visible at Open. The snapshot pins what the form
		// renders WHEN the guard is true; the absence-case is covered
		// implicitly by other form snapshots that don't pass a world.
		{
			name:  "form_per_field_when_toggle",
			width: 80,
			world: map[string]any{"show": true},
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "form",
				ChoicePrompt: "Compose",
				ChoiceIntent: "submit",
				ChoiceFields: []app.ChoiceField{
					{Name: "name", Type: "string", Placeholder: "type a name"},
					// Visible only when world.show — flipped on for
					// this snapshot to lock the "guard true" render.
					{Name: "extra", Type: "string", Placeholder: "extra info", When: "world.show == true"},
				},
			},
		},

		// Width floor — the widget clamps width to 20 (choice_widget.go
		// :View). Lock the layout at exactly that floor so a future
		// regression that shifts the clamp shows up as a diff.
		{
			name:  "width_floor",
			width: 20,
			element: app.ViewElement{
				Kind:         "choice",
				ChoiceMode:   "single",
				ChoicePrompt: "Pick",
				ChoiceItems: []app.ChoiceItem{
					{Label: "A", Intent: "pick_a"},
					{Label: "B", Intent: "pick_b"},
				},
			},
		},
	}

	goldenDir := filepath.Join("testdata", "choice_widget_golden")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tui.NewTestChoiceWidget()
			require.NoError(t, w.OpenChoice(tc.element, tc.world), "open widget")

			for _, k := range tc.keys {
				w.SendKey(k)
			}
			for _, r := range tc.runes {
				w.SendRune(r)
			}

			got := w.View(tc.width)
			goldenPath := filepath.Join(goldenDir, tc.name+".golden")

			if *flagUpdate {
				require.NoError(t, os.MkdirAll(goldenDir, 0o755))
				require.NoError(t, os.WriteFile(goldenPath, []byte(got), 0o644))
				t.Logf("updated golden %s", goldenPath)
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("golden file %s not found (run with -update to create): %v", goldenPath, err)
			}
			require.Equal(t, string(want), got,
				"widget View output drifted from golden %s — re-run with -update if the change is intentional", goldenPath)
		})
	}
}
