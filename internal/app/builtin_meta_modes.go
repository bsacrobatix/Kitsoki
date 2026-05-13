package app

import "os"

// builtinMetaModes returns the meta_modes that ship with kitsoki and are
// available to every app without YAML declaration. An app declares a
// meta_mode with the same name to override one — the injection step
// only fills entries that aren't already present.
//
// `self` keys against a synthetic `kitsoki-self` app_id at chat-resolve
// time so the conversation persists across apps (a `self` session
// started while playing cloak is the same row the user reopens while
// playing dev-story). That special-case lives in
// internal/metamode/controller.go where the scope-key tuple is built.
//
// `bug` keys per-app — every app has its own pile of bug reports under
// `bugs/`. No special-case needed.
//
// `self` is only injected when $KITSOKI_REPO is set, because the
// engineer agent needs an unambiguous cwd. Without the env var,
// failing app loads everywhere (tests, CI, anyone running kitsoki on
// a release binary without dev workspace) would be a worse outcome
// than silently dropping the mode. Users who want `self` set the env
// var; apps that want it without the env var declare it explicitly.
func builtinMetaModes() map[string]*MetaModeDef {
	onpath := &MetaReturnDef{Intent: "onpath"}
	out := map[string]*MetaModeDef{
		"bug": {
			Trigger: "bug",
			Label:   "File a bug",
			Banner:  "Filing a bug report — write it down and the agent files it under bugs/.",
			Agent:   "bug-reporter",
			Return:  onpath,
		},
	}
	if _, ok := os.LookupEnv("KITSOKI_REPO"); ok {
		out["self"] = &MetaModeDef{
			Trigger: "self",
			Label:   "Edit kitsoki",
			Banner:  "Editing kitsoki itself — your changes affect the engine, not the running story.",
			Agent:   "kitsoki-engineer",
			Cwd:     "${KITSOKI_REPO}",
			Return:  onpath,
		}
	}
	return out
}

// injectBuiltinMetaModes adds any builtin meta mode whose name isn't
// already present in def.MetaModes. Called between merge and validate
// in both load paths so the validator sees the full effective set —
// trigger collisions between an app's mode and a builtin show up as
// regular validation errors rather than silent overrides.
//
// Apps override a builtin by declaring a meta_mode with the same key
// in their YAML (story-author-style); declaration wins over injection.
// The function is a no-op when def is nil.
func injectBuiltinMetaModes(def *AppDef) {
	if def == nil {
		return
	}
	if def.MetaModes == nil {
		def.MetaModes = make(map[string]*MetaModeDef, len(builtinMetaModes()))
	}
	for name, mode := range builtinMetaModes() {
		if _, exists := def.MetaModes[name]; exists {
			continue
		}
		def.MetaModes[name] = mode
	}
}
