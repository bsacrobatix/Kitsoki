package harness_test

import (
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hally/internal/app"
	"hally/internal/harness"
)

// compileSchema compiles raw bytes via santhosh-tekuri/jsonschema to confirm
// the document is well-formed. Unknown formats (e.g. `jql`) are registered
// permissively so format checks degrade to no-op rather than tripping the
// compiler.
func compileSchema(t *testing.T, raw []byte) {
	t.Helper()

	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc))

	c := jsonschema.NewCompiler()
	c.RegisterFormat(&jsonschema.Format{
		Name:     "jql",
		Validate: func(any) error { return nil },
	})
	require.NoError(t, c.AddResource("test://schema.json", doc))
	_, err := c.Compile("test://schema.json")
	require.NoError(t, err, "generated schema must compile")
}

func TestBuildTransitionSchema(t *testing.T) {
	type want struct {
		intentEnum     []string // expected order in intent.enum
		hasAllOf       bool
		allOfLen       int     // when hasAllOf
		slotDefs       []string // names that must appear under $defs (slots__<name>)
		assertSlotProp func(t *testing.T, root map[string]any)
	}

	tests := []struct {
		name           string
		appDef         *app.AppDef
		allowedIntents []string
		want           want
	}{
		{
			name: "empty intent list",
			appDef: &app.AppDef{
				App:     app.AppMeta{ID: "x"},
				Intents: map[string]app.Intent{},
			},
			allowedIntents: nil,
			want: want{
				intentEnum: []string{},
				hasAllOf:   false,
			},
		},
		{
			name: "one intent no slots",
			appDef: &app.AppDef{
				App: app.AppMeta{ID: "x"},
				Intents: map[string]app.Intent{
					"look": {Title: "Look"},
				},
			},
			allowedIntents: []string{"look"},
			want: want{
				intentEnum: []string{"look"},
				hasAllOf:   false,
			},
		},
		{
			name: "one intent required string slot with format jql",
			appDef: &app.AppDef{
				App: app.AppMeta{ID: "x"},
				Intents: map[string]app.Intent{
					"search": {
						Slots: map[string]app.Slot{
							"jql": {
								Type:        "string",
								Required:    true,
								Format:      "jql",
								Description: "JQL query",
							},
						},
					},
				},
			},
			allowedIntents: []string{"search"},
			want: want{
				intentEnum: []string{"search"},
				hasAllOf:   true,
				allOfLen:   1,
				slotDefs:   []string{"slots__search"},
				assertSlotProp: func(t *testing.T, root map[string]any) {
					defs := root["$defs"].(map[string]any)
					def := defs["slots__search"].(map[string]any)
					props := def["properties"].(map[string]any)
					jqlProp := props["jql"].(map[string]any)
					assert.Equal(t, "jql", jqlProp["format"])
					assert.Equal(t, "string", jqlProp["type"])
					assert.Equal(t, "JQL query", jqlProp["description"])
					req := def["required"].([]any)
					require.Len(t, req, 1)
					assert.Equal(t, "jql", req[0])
				},
			},
		},
		{
			name: "multiple intents some with slots",
			appDef: &app.AppDef{
				App: app.AppMeta{ID: "x"},
				Intents: map[string]app.Intent{
					"look": {Title: "Look"},
					"go": {
						Slots: map[string]app.Slot{
							"direction": {
								Type:   "string",
								Values: []string{"north", "south"},
							},
						},
					},
					"hang_cloak": {Title: "Hang"},
				},
			},
			allowedIntents: []string{"look", "go", "hang_cloak"},
			want: want{
				intentEnum: []string{"look", "go", "hang_cloak"},
				hasAllOf:   true,
				allOfLen:   1, // only `go` has slots
				slotDefs:   []string{"slots__go"},
				assertSlotProp: func(t *testing.T, root map[string]any) {
					defs := root["$defs"].(map[string]any)
					_, hasGo := defs["slots__go"]
					assert.True(t, hasGo, "slots__go should exist")
					_, hasLook := defs["slots__look"]
					assert.False(t, hasLook, "slots__look should not exist")
				},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			raw, err := harness.BuildTransitionSchema(tc.appDef, tc.allowedIntents)
			require.NoError(t, err)

			var root map[string]any
			require.NoError(t, json.Unmarshal(raw, &root))

			// Top-level invariants.
			assert.Equal(t, "object", root["type"])
			assert.Equal(t, false, root["additionalProperties"])

			props := root["properties"].(map[string]any)
			intentProp := props["intent"].(map[string]any)
			assert.Equal(t, "string", intentProp["type"])

			enum, _ := intentProp["enum"].([]any)
			assert.Len(t, enum, len(tc.want.intentEnum))
			for i, name := range tc.want.intentEnum {
				assert.Equal(t, name, enum[i])
			}

			if tc.want.hasAllOf {
				allOf, ok := root["allOf"].([]any)
				require.True(t, ok, "allOf should be present")
				assert.Len(t, allOf, tc.want.allOfLen)

				defs, ok := root["$defs"].(map[string]any)
				require.True(t, ok)
				for _, name := range tc.want.slotDefs {
					_, present := defs[name]
					assert.True(t, present, "$defs.%s should be present", name)
				}
			} else {
				_, hasAllOf := root["allOf"]
				assert.False(t, hasAllOf, "allOf should be absent")
			}

			if tc.want.assertSlotProp != nil {
				tc.want.assertSlotProp(t, root)
			}

			compileSchema(t, raw)
		})
	}
}

// TestBuildTransitionSchema_NilAppDef confirms a nil appDef is a programmer
// error rather than a silent empty schema.
func TestBuildTransitionSchema_NilAppDef(t *testing.T) {
	_, err := harness.BuildTransitionSchema(nil, []string{"x"})
	require.Error(t, err)
}
