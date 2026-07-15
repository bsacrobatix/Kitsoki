#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$root/.artifacts/pog-driver-acceptance"
tmp="$(mktemp -d "$root/.artifacts/pog-driver-acceptance/run.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

app="$tmp/pog-driver-app.yaml"
config="$tmp/kitsoki.yaml"
cat >"$config" <<'YAML'
default_profile: pog-driver-claude
story_dirs:
  - ./stories
harness_profiles:
  pog-driver-claude:
    backend: claude
    model: opus
    models: [opus]
    effort: medium
    efforts: [low, medium, high, xhigh, max]
  pog-driver-codex:
    backend: codex
    model: gpt-5.5
    models: [gpt-5.5]
    effort: medium
    efforts: [low, medium, high, xhigh, max]
YAML

cat >"$app" <<'YAML'
app: { id: pog-driver-acceptance, version: 0.1.0, title: POG Driver Acceptance }
hosts: [host.agent.task]
agents:
  pog-driver:
    system_prompt: "Use only the scoped graph MCP. Propose graph changes only."
    tools: []
    mcp:
      servers:
        kitsoki-graph:
          command: kitsoki
          args: [mcp-graph, --catalog, pog/catalog.yaml]
      tools: ["mcp__kitsoki-graph__*"]
world: {}
intents: {}
root: idle
states:
  idle: { view: "idle" }
YAML

check_profile() {
  local profile="$1"
  local backend="$2"
  local model="$3"
  local out="$tmp/$profile.json"

  KITSOKI_AGENT_CLAUDE_BIN=/bin/echo \
  KITSOKI_AGENT_CODEX_BIN=/bin/echo \
    go run ./cmd/kitsoki agent launch \
      --config "$config" \
      --app "$app" \
      --agent pog-driver \
      --profile "$profile" \
      --working-dir "$root" \
      --task "Inspect the graph and propose only." >"$out"

  node - "$out" "$profile" "$backend" "$model" <<'NODE'
const fs = require("fs");
const [path, profile, backend, model] = process.argv.slice(2);
const plan = JSON.parse(fs.readFileSync(path, "utf8"));
function fail(msg) {
  console.error(msg);
  process.exit(1);
}
if (plan.profile !== profile) fail(`profile ${plan.profile} != ${profile}`);
if (plan.backend !== backend) fail(`backend ${plan.backend} != ${backend}`);
if (plan.model !== model) fail(`model ${plan.model} != ${model}`);
const joined = (plan.command || []).join(" ");
if (!joined.includes("mcp_servers.kitsoki-graph.enabled=true") && !joined.includes("--mcp-config")) {
  fail("graph MCP attachment missing from launch command");
}
if (backend === "codex" && !joined.includes("--disable=shell_tool")) {
  fail("unexpected codex shell-tool shape");
}
NODE
}

check_profile pog-driver-claude claude opus
check_profile pog-driver-codex codex gpt-5.5

echo "pog driver launch profile acceptance passed"
