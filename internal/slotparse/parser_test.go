// Tests for the [For] dispatcher and the [Parser] interface contract.
package slotparse

import (
	"testing"

	"kitsoki/internal/app"
)

func TestFor_Dispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		slotType  string
		wantNil   bool
		probeIn   string
		wantValue any
		wantOK    bool
	}{
		{"int_dispatch", "int", false, "six", 6, true},
		{"money_dispatch", "money", false, "120 dollars", 120, true},
		{"dollar_int_dispatch_alias", "$int", false, "120 dollars", 120, true},
		{"bool_dispatch", "bool", false, "yes", true, true},
		// list and date land in Phase 7; nil-check rows kept as
		// "type=list" (no inner) and "type=" / "weirdtype" miss cases.
		{"bare_list_returns_nil", "list", true, "", nil, false},
		{"empty_type_returns_nil", "", true, "", nil, false},
		{"unknown_type_returns_nil", "weirdtype", true, "", nil, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := For(app.Slot{Type: tc.slotType})
			if tc.wantNil {
				if p != nil {
					t.Errorf("For(Slot{Type:%q}): want nil, got %T", tc.slotType, p)
				}
				return
			}
			if p == nil {
				t.Fatalf("For(Slot{Type:%q}): want non-nil parser, got nil", tc.slotType)
			}
			got := p.Parse(tok(t, tc.probeIn), app.Slot{Type: tc.slotType})
			if got.OK != tc.wantOK {
				t.Errorf("For(%q).Parse(%q): OK=%v want %v", tc.slotType, tc.probeIn, got.OK, tc.wantOK)
			}
			if got.Value != tc.wantValue {
				t.Errorf("For(%q).Parse(%q): Value=%v want %v", tc.slotType, tc.probeIn, got.Value, tc.wantValue)
			}
		})
	}
}

// TestFor_EnumDispatchUsesSlotValues asserts [For] hands the slot
// metadata (Values + Synonyms) through to the enum parser — easy to
// break by stripping the Slot in the adapter.
func TestFor_EnumDispatchUsesSlotValues(t *testing.T) {
	t.Parallel()
	slot := fixtureProfessionSlot()
	p := For(slot)
	if p == nil {
		t.Fatalf("For(enum slot): want non-nil parser")
	}
	got := p.Parse(tok(t, "rich guy"), slot)
	if !got.OK || got.Value != "banker" {
		t.Errorf("For(enum slot).Parse(%q): want (\"banker\", true), got %+v", "rich guy", got)
	}
}

// TestFor_DateDispatch confirms For(Slot{Type:"date"}) hands back a
// non-nil parser that returns OK on a recognised phrase. The exact
// value isn't checked — date_test.go owns that. This test exists so a
// future change to the For dispatch table that drops date is caught
// here instead of only in date_test.go.
func TestFor_DateDispatch(t *testing.T) {
	t.Parallel()
	p := For(app.Slot{Type: "date"})
	if p == nil {
		t.Fatalf("For(Slot{Type:\"date\"}): want non-nil parser")
	}
	got := p.Parse(tok(t, "tomorrow"), app.Slot{Type: "date"})
	if !got.OK {
		t.Errorf("For(date).Parse(\"tomorrow\"): want OK, got %+v", got)
	}
}

// TestFor_ListDispatch confirms the suffix-parsing path:
// For(Slot{Type:"list[int]"}) returns a non-nil parser that calls
// through to ParseList(intParser).
func TestFor_ListDispatch(t *testing.T) {
	t.Parallel()
	slot := app.Slot{Type: "list[int]"}
	p := For(slot)
	if p == nil {
		t.Fatalf("For(Slot{Type:\"list[int]\"}): want non-nil parser")
	}
	got := p.Parse(tok(t, "6, 12, and 3"), slot)
	if !got.OK {
		t.Fatalf("list[int].Parse: want OK, got %+v", got)
	}
	vals, ok := got.Value.([]any)
	if !ok || len(vals) != 3 {
		t.Errorf("list[int] vals=%v, want [6 12 3]", got.Value)
	}
}

// TestTokenRange_HalfOpen pins the documented semantics of TokenRange:
// tokens[Start:End] is the consumed slice. A single-token consume is
// {Start: i, End: i+1}.
func TestTokenRange_HalfOpen(t *testing.T) {
	t.Parallel()
	got := ParseInt(tok(t, "6"))
	if !got.OK {
		t.Fatalf("ParseInt(\"6\"): want OK=true")
	}
	if len(got.Consumed) != 1 || got.Consumed[0] != (TokenRange{Start: 0, End: 1}) {
		t.Errorf("ParseInt(\"6\"): want Consumed=[{0 1}], got %+v", got.Consumed)
	}
}
