package statedir

import "testing"

func TestRootUnset(t *testing.T) {
	t.Setenv(EnvStateDir, "")
	if dir, ok := Root(); ok || dir != "" {
		t.Fatalf("Root() with unset env = (%q, %v), want (\"\", false)", dir, ok)
	}
}

func TestRootBlankIsUnset(t *testing.T) {
	t.Setenv(EnvStateDir, "   ")
	if dir, ok := Root(); ok || dir != "" {
		t.Fatalf("Root() with blank env = (%q, %v), want (\"\", false)", dir, ok)
	}
}

func TestRootSet(t *testing.T) {
	t.Setenv(EnvStateDir, "/mnt/state")
	dir, ok := Root()
	if !ok || dir != "/mnt/state" {
		t.Fatalf("Root() = (%q, %v), want (\"/mnt/state\", true)", dir, ok)
	}
}

func TestRootTrimsWhitespace(t *testing.T) {
	t.Setenv(EnvStateDir, "  /mnt/state \n")
	dir, ok := Root()
	if !ok || dir != "/mnt/state" {
		t.Fatalf("Root() = (%q, %v), want trimmed (\"/mnt/state\", true)", dir, ok)
	}
}
