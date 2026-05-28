// Package app — oracle plugin declaration loader (proposal §2 Phase B-2).
//
// resolveOraclePlugins is called after parseAndMerge / resolveImports to:
//  1. Validate every OraclePluginDecl in def.OraclePlugins.
//  2. Perform single-pass ${VAR} substitution in each plugin's Env and Headers
//     maps (proposal §2 resolution 4). Unset env vars are hard errors.
//  3. Inject a default "oracle.claude" entry (plugin: builtin.claude_cli) when
//     the story omits oracle_plugins entirely or omits oracle.claude specifically,
//     so all existing stories run unchanged.
//
// The resolved declarations stay on def.OraclePlugins; the host.OracleRegistry
// is built from them by the orchestrator at session construction.
package app

import (
	"fmt"
	"os"
	"strings"
)

// knownB2Plugins is the set of plugin values that are fully supported in B-2.
var knownB2Plugins = map[string]bool{
	"builtin.claude_cli": true,
	"builtin.inprocess":  true,
}

// resolveOraclePlugins validates and resolves all oracle plugin declarations.
// It must be called after parseAndMerge. Errors are appended to *errs.
func resolveOraclePlugins(def *AppDef, file string) []error {
	var errs []error
	addErr := func(msg string) {
		errs = append(errs, &ValidationError{File: file, Message: msg})
	}

	if def.OraclePlugins == nil {
		def.OraclePlugins = make(map[string]*OraclePluginDecl)
	}

	// Validate and resolve each declared plugin.
	for name, decl := range def.OraclePlugins {
		if decl == nil {
			addErr(fmt.Sprintf("oracle_plugins.%s: empty declaration", name))
			continue
		}
		if !strings.HasPrefix(name, "oracle.") {
			addErr(fmt.Sprintf("oracle_plugins: key %q must start with 'oracle.' prefix", name))
			continue
		}
		if decl.Plugin == "" {
			addErr(fmt.Sprintf("oracle_plugins.%s: plugin: is required", name))
			continue
		}
		if !knownB2Plugins[decl.Plugin] {
			// B-3 stubs: recognise but mark as unsupported.
			switch decl.Plugin {
			case "subprocess", "mcp_http":
				addErr(fmt.Sprintf("oracle_plugins.%s: plugin %q not yet supported in phase B-2 — coming in B-3", name, decl.Plugin))
				continue
			default:
				addErr(fmt.Sprintf("oracle_plugins.%s: unknown plugin %q (supported in B-2: builtin.claude_cli, builtin.inprocess)", name, decl.Plugin))
				continue
			}
		}
		// Interpolate ${VAR} in Env map.
		for k, v := range decl.Env {
			expanded, missing := expandEnvVar(v)
			if missing != "" {
				addErr(fmt.Sprintf("oracle_plugins.%s: env var %s referenced in env.%s not set", name, missing, k))
				continue
			}
			decl.Env[k] = expanded
		}
		// Interpolate ${VAR} in Headers map.
		for k, v := range decl.Headers {
			expanded, missing := expandEnvVar(v)
			if missing != "" {
				addErr(fmt.Sprintf("oracle_plugins.%s: env var %s referenced in headers.%s not set", name, missing, k))
				continue
			}
			decl.Headers[k] = expanded
		}
	}

	if len(errs) > 0 {
		return errs
	}

	// Inject default oracle.claude if missing.
	if _, hasDefault := def.OraclePlugins["oracle.claude"]; !hasDefault {
		def.OraclePlugins["oracle.claude"] = &OraclePluginDecl{Plugin: "builtin.claude_cli"}
	}

	return nil
}

// expandEnvVar performs a single-pass ${VAR} substitution in s.
// Returns (expanded, "") on success.
// Returns ("", "VAR") when any ${VAR} token references an unset env var.
// Per proposal §2 resolution 4: substitution is single-pass; literal ${
// values that remain after expansion pass through verbatim.
func expandEnvVar(s string) (expanded, missing string) {
	result := s
	for {
		start := strings.Index(result, "${")
		if start < 0 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end < 0 {
			break // no closing brace — treat as literal
		}
		end += start
		varName := result[start+2 : end]
		val, ok := os.LookupEnv(varName)
		if !ok {
			return "", varName
		}
		result = result[:start] + val + result[end+1:]
	}
	return result, ""
}
