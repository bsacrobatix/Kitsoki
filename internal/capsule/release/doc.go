// Package release owns immutable, content-addressed release candidates.
//
// A candidate records exact source and runtime inputs. It is deliberately
// independent from reviewwave so a frozen wave can be represented by its
// digest without introducing a control-plane package cycle.
package release
