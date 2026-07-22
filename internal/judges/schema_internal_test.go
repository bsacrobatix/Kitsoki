package judges

import (
	"net/url"
	"testing"
)

// TestSchemaURLIsAbsolute pins the reason schemaURL carries an explicit
// scheme. A relative resource id is resolved against the process working
// directory, so registering it makes package init touch the filesystem — and
// every kitsoki command then panics before main() when that directory cannot
// be read. That is not hypothetical: `ssh root@host 'runuser -u pog --
// kitsoki daemon invite ...'` inherits root's /root, which pog cannot read,
// and the whole CLI died with `judges: register embedded schema: error in
// parsing "judge_verdict.json": stat .: permission denied`.
//
// The failure only reproduces where getcwd() walks parent directories (Linux,
// as on the deployed host) — not on darwin — so the invariant is asserted
// directly rather than by staging an unreadable cwd.
func TestSchemaURLIsAbsolute(t *testing.T) {
	u, err := url.Parse(schemaURL)
	if err != nil {
		t.Fatalf("parse schemaURL %q: %v", schemaURL, err)
	}
	if u.Scheme == "" {
		t.Fatalf("schemaURL %q is a relative reference; the compiler will resolve it against the working directory", schemaURL)
	}
	if got := mustCompileSchema(); got == nil {
		t.Fatal("mustCompileSchema returned nil")
	}
}
