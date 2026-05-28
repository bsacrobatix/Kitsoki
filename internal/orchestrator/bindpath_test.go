package orchestrator

import (
	"reflect"
	"testing"
)

func TestLookupBindPath(t *testing.T) {
	data := map[string]any{
		"stdout": "hello",
		"submitted": map[string]any{
			"illness":  "cholera",
			"severity": 3,
			"names":    []any{"Adam", "Beth", "Carol", "Daniel", "Edith"},
			"members": []any{
				map[string]any{"role": "leader", "name": "Adam"},
				map[string]any{"role": "scout", "name": "Beth"},
			},
		},
	}

	cases := []struct {
		name   string
		path   string
		want   any
		wantOK bool
	}{
		{"top level", "stdout", "hello", true},
		{"missing top", "missing", nil, false},
		{"dotted", "submitted.illness", "cholera", true},
		{"dotted missing", "submitted.nope", nil, false},
		{"index 0", "submitted.names[0]", "Adam", true},
		{"index 4", "submitted.names[4]", "Edith", true},
		{"index out of range", "submitted.names[5]", nil, false},
		{"negative index", "submitted.names[-1]", nil, false},
		{"index into object array", "submitted.members[1].name", "Beth", true},
		{"chained indices unsupported", "submitted.names[0][1]", nil, false},
		{"malformed segment", "submitted.names[", nil, false},
		{"empty path", "", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lookupBindPath(data, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (val=%v)", ok, tc.wantOK, got)
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}
