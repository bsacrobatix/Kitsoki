package appdef

import (
	"context"
	"sync/atomic"

	"kitsoki/internal/app"
)

// Loader compiles an in-memory definition closure into a live *app.AppDef.
// The returned Handle owns the materialised tree; Release retires it. This
// is the injected seam: production uses DefaultLoader (app.LoadFromFiles); a
// test can substitute a loader that fails on demand without writing a
// broken fixture to disk.
type Loader interface {
	Load(ctx context.Context, c Closure) (*Handle, error)
}

// Handle is a compiled, live definition. Def is safe to serve until Release
// is called; the materialised tree behind Def.BaseDir is removed at that
// point, so Release must happen only after nothing renders or dispatches
// against Def. Release is idempotent — calling it more than once (or
// concurrently) is safe and only the first call has effect.
type Handle struct {
	Def    *app.AppDef
	Digest string

	release  func()
	released atomic.Bool
}

// newHandle wraps def with release as its (idempotent) teardown.
func newHandle(def *app.AppDef, digest string, release func()) *Handle {
	return &Handle{Def: def, Digest: digest, release: release}
}

// Release retires the handle, removing its materialised tree. Safe to call
// more than once; only the first call has effect.
func (h *Handle) Release() {
	if h == nil {
		return
	}
	if h.released.CompareAndSwap(false, true) && h.release != nil {
		h.release()
	}
}

// DefaultLoader compiles through app.LoadFromFiles, which materialises the
// closure to a private temp tree and re-runs the ordinary loader — the same
// validation a story on disk gets, no shortcuts.
type DefaultLoader struct{}

// Load implements Loader.
func (DefaultLoader) Load(_ context.Context, c Closure) (*Handle, error) {
	def, cleanup, err := app.LoadFromFiles(c.Files, c.Entry)
	if err != nil {
		cleanup()
		return nil, err
	}
	return newHandle(def, "", cleanup), nil
}
