import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { cloneFixture, removeFixture, setFreshTimestamps, manifestPath, readManifestJSON, writeManifestJSON } from "./helpers/fixture.mjs";

const TOOL_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DOCTOR_CLI = path.join(TOOL_ROOT, "scripts", "demo-doctor.mjs");
const RECORD_TOUR_CLI = path.join(TOOL_ROOT, "scripts", "record-tour.mjs");

function withFixture(mutate, fn) {
  const dir = cloneFixture();
  if (mutate) mutate(dir);
  setFreshTimestamps(dir);
  try {
    return fn(dir);
  } finally {
    removeFixture(dir);
  }
}

test("demo-doctor CLI: human output exits 0 with one ok/FAIL line per check on a clean fixture", () => {
  withFixture(null, (dir) => {
    const res = spawnSync(process.execPath, [DOCTOR_CLI, manifestPath(dir)], { encoding: "utf8" });
    assert.equal(res.status, 0, res.stdout + res.stderr);
    const lines = res.stdout.trim().split("\n");
    assert.equal(lines.length, 5);
    for (const line of lines) assert.match(line, /^ok {2}/);
  });
});

test("demo-doctor CLI: --json exits non-zero and emits a machine report on failure", () => {
  withFixture(
    (dir) => {
      const manifest = readManifestJSON(dir);
      manifest.slidey = "fake-slidey-flags";
      writeManifestJSON(dir, manifest);
    },
    (dir) => {
      const res = spawnSync(process.execPath, [DOCTOR_CLI, manifestPath(dir), "--json"], { encoding: "utf8" });
      assert.equal(res.status, 1, res.stdout + res.stderr);
      const report = JSON.parse(res.stdout);
      assert.equal(report.ok, false);
      assert.ok(report.checks.some((c) => c.name === "estimate" && !c.ok));
    }
  );
});

test("record-tour CLI: exits 0 and prints a JSON summary on a clean fixture", () => {
  withFixture(null, (dir) => {
    const res = spawnSync(process.execPath, [RECORD_TOUR_CLI, manifestPath(dir)], { encoding: "utf8" });
    assert.equal(res.status, 0, res.stdout + res.stderr);
    const summary = JSON.parse(res.stdout);
    assert.equal(summary.ok, true);
    assert.equal(summary.doctor.ok, true);
  });
});

test("record-tour CLI: exits non-zero when the loop fails", () => {
  withFixture(
    (dir) => {
      const manifest = readManifestJSON(dir);
      manifest.slidey = "fake-slidey-badjson";
      writeManifestJSON(dir, manifest);
    },
    (dir) => {
      const res = spawnSync(process.execPath, [RECORD_TOUR_CLI, manifestPath(dir)], { encoding: "utf8" });
      assert.equal(res.status, 1);
      assert.match(res.stderr, /unavailable/);
    }
  );
});
