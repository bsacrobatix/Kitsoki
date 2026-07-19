#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const [root, manifestName = "manifest.json", expectedPogSha = ""] = process.argv.slice(2);
if (!root || !["manifest.json", ".hosted-pog-local-state.json"].includes(manifestName)) {
  console.error("usage: state-content-digest.mjs ROOT [manifest.json|.hosted-pog-local-state.json] [EXPECTED_POG_SHA]");
  process.exit(2);
}

let manifest;
try {
  manifest = JSON.parse(fs.readFileSync(path.join(root, manifestName), "utf8"));
} catch (error) {
  console.error(`could not read state manifest: ${error.message}`);
  process.exit(2);
}
if (
  manifest.schema !== "kitsoki/hosted-pog-local-state/v1" ||
  !/^[0-9a-f]{64}$/.test(manifest.content_sha256 ?? "") ||
  (expectedPogSha && manifest.pog_sha !== expectedPogSha)
) {
  console.error("state manifest contract is invalid");
  process.exit(2);
}

const ignoredRootFiles = new Set([manifestName, ".hosted-pog-local-state.sha256"]);
const ignoredRuntimeFiles = new Set(["agent-runner/sessions.db-shm"]);
const hash = crypto.createHash("sha256");
let fileCount = 0;
let byteCount = 0;

function walk(directory, prefix = "") {
  const entries = fs.readdirSync(directory, { withFileTypes: true })
    .filter((entry) => prefix !== "" || !ignoredRootFiles.has(entry.name))
    .sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  for (const entry of entries) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    const absolute = path.join(directory, entry.name);
    if (
      ignoredRuntimeFiles.has(relative) ||
      (relative === "agent-runner/sessions.db-wal" && entry.isFile() && fs.statSync(absolute).size === 0)
    ) {
      continue;
    }
    if (entry.isDirectory()) {
      walk(absolute, relative);
    } else if (entry.isFile()) {
      const content = fs.readFileSync(absolute);
      fileCount += 1;
      byteCount += content.length;
      hash.update(relative);
      hash.update("\0");
      hash.update(String(content.length));
      hash.update("\0");
      hash.update(content);
    } else {
      console.error(`state contains a link or special file: ${relative}`);
      process.exit(3);
    }
  }
}

walk(root);
const actual = hash.digest("hex");
if (
  actual !== manifest.content_sha256 ||
  (Number.isInteger(manifest.file_count) && fileCount !== manifest.file_count) ||
  (Number.isInteger(manifest.byte_count) && byteCount !== manifest.byte_count)
) {
  console.error(JSON.stringify({
    expected: manifest.content_sha256,
    actual,
    expected_file_count: manifest.file_count,
    actual_file_count: fileCount,
    expected_byte_count: manifest.byte_count,
    actual_byte_count: byteCount,
  }));
  process.exit(4);
}

process.stdout.write(actual);
