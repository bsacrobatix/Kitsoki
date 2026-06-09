---
name: starlark
description: Write, review, and validate Starlark — the small deterministic Python dialect (go.starlark.net runtime) being embedded in kitsoki. Use when authoring or debugging a `.star`/`.bzl` module, embedding the Starlark interpreter in Go (Thread, FileOptions, custom Value types, exposing Go builtins), choosing a validation toolchain (buildifier / starcheck / starlark CLI), or diagnosing a "this is valid Python but errors in Starlark" surprise (strings aren't iterable, no while/recursion/classes/exceptions, globals can't be reassigned). General + runtime-focused; a kitsoki `host.star.*` section will be added once that feature lands.
---

# Starlark

Starlark is a small, deterministic, hermetic **dialect of Python** built for
embedded configuration/scripting (it powers Bazel). kitsoki is adding Starlark
as a deterministic host capability — see
[`docs/proposals/starlark-host.md`](../../proposals/starlark-host.md) — running
on the canonical Go runtime, **`go.starlark.net`**.

This skill is general (language + Go embedding + validation). The kitsoki-specific
`host.star.*` surface, `CONTRACT` shape, and determinism levels will be added
here once that feature is implemented.

## Reference (read on demand)

| File | When you need it |
|---|---|
| [`reference/language.md`](reference/language.md) | Language semantics + the **Python-3 → Starlark divergence cheatsheet** (the gotchas) |
| [`reference/go-runtime.md`](reference/go-runtime.md) | Embedding API: `Thread`, `ExecFileOptions`, `Value`/custom types, exposing Go builtins, dialect flags, running untrusted code safely |
| [`reference/validation.md`](reference/validation.md) | The validation toolchain — buildifier, `starcheck`, the `starlark` CLI |

## The five things that bite a Python author

Reach for the cheatsheet, but these cause most "valid Python, broken Starlark":

1. **Strings are NOT iterable.** `for c in "ab"` / `list("ab")` are errors. Use
   `"ab".elems()` / `.codepoints()`. This is the #1 surprise.
2. **Globals can't be reassigned.** `X = 1; X = 2` is a *static* error. Mutate
   the contents of a mutable global, don't rebind the name.
3. **No `while`, no recursion** (by default), and loops must be over finite
   sequences — Starlark is deliberately not Turing-complete.
4. **No exceptions.** No `try`/`except`/`raise`. Errors abort; `fail(msg)` aborts
   on purpose. Validate inputs up front.
5. **No classes, no `import`.** Use plain `def`/dicts; cross-module sharing is
   `load("//pkg:file.star", "name")`, resolved by the host, not the filesystem.

> **Hermetic ≠ safe for untrusted code.** Determinism/hermeticity bound *what*
> the language reaches, not CPU/memory. Untrusted execution needs step limits +
> cancellation + a frozen allowlisted environment — see
> [`go-runtime.md`](reference/go-runtime.md#running-untrusted-code).

## The validation loop

Validate **without executing** — safe, side-effect-free, and it catches the
errors that matter for an embedded config language.

```bash
# 1. format + lint (if buildifier is installed)
buildifier -type=default -mode=check -lint=warn module.star

# 2. parse + resolve (no execution). From docs/skills/starlark/tools/starcheck:
go run . module.star
go run . -r scripts/                          # a whole tree
go run . -predeclared=world,http,secret f.star # only these builtins are granted

# both at once, over a path:
docs/skills/starlark/tools/validate.sh scripts/
```

`starcheck` is the tool to own: it wraps `syntax.Parse` + `resolve.File`, so by
restricting `-predeclared` to a capability level's allowed names you can prove at
compile time that a function references nothing outside that level — the exact
check the [`starlark-host` proposal](../../proposals/starlark-host.md) relies on.
Details and flags: [`reference/validation.md`](reference/validation.md).

## Embedding it in Go (the short version)

```go
opts := &syntax.FileOptions{}                  // zero value = spec-strict dialect
thread := &starlark.Thread{Name: "load", Print: myPrint}
thread.SetMaxExecutionSteps(1_000_000)         // budget for untrusted code
globals, err := starlark.ExecFileOptions(opts, thread, "config.star", src, predeclared)
v, err := starlark.Call(thread, globals["fn"], starlark.Tuple{starlark.String("x")}, nil)
```

- Use the `*Options` entry points — `ExecFileOptions`/`EvalOptions`. Plain
  `ExecFile`/`Eval` are **deprecated** (they read legacy global flags).
- Expose Go functions with `starlark.NewBuiltin` + `starlark.UnpackArgs`.
- Hand structured data in via `starlarkstruct.Struct`, or a custom `Value` type
  implementing the optional interfaces (`HasAttrs`, `Mapping`, `Iterable`, …).
- Pass per-call context through `thread.SetLocal`/`Local`, not function args.

Full API surface, custom-type interface signatures, the `load()` caching
contract, and the dialect-flag table: [`reference/go-runtime.md`](reference/go-runtime.md).

## Authoritative sources

- Language spec: <https://github.com/bazelbuild/starlark/blob/master/spec.md>
- **Go-runtime dialect spec** (the one that governs our runtime):
  <https://github.com/google/starlark-go/blob/master/doc/spec.md>
- godoc: <https://pkg.go.dev/go.starlark.net/starlark>
- Ecosystem index: <https://github.com/laurentlb/awesome-starlark>
