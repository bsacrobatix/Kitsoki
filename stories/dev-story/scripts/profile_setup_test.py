#!/usr/bin/env python3
"""Unit tests for local harness profile setup helpers."""

from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path

from profile_setup_lib import apply_candidate, discover


class ProfileSetupTest(unittest.TestCase):
    def test_discover_merges_profiles_and_redacts_env_refs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".kitsoki.yaml").write_text(
                """default_profile: claude-native
harness_profiles:
  claude-native:
    backend: claude
    model: opus
    models: [opus, sonnet]
""",
                encoding="utf-8",
            )
            (root / ".kitsoki.local.yaml").write_text(
                """default_profile: synthetic-codex
harness_profiles:
  synthetic-codex:
    backend: codex
    model: hf:test/model
    models: [hf:test/model]
    env:
      OPENAI_BASE_URL: https://example.invalid/v1
      OPENAI_API_KEY: "${SYNTHETIC_API_KEY}"
""",
                encoding="utf-8",
            )
            old = os.environ.get("SYNTHETIC_API_KEY")
            os.environ["SYNTHETIC_API_KEY"] = "sk-secret"
            try:
                report = discover(root)
            finally:
                if old is None:
                    os.environ.pop("SYNTHETIC_API_KEY", None)
                else:
                    os.environ["SYNTHETIC_API_KEY"] = old
            self.assertEqual(report["default_profile"], "synthetic-codex")
            names = [profile["name"] for profile in report["profiles"]]
            self.assertEqual(names, ["claude-native", "synthetic-codex"])
            synthetic = next(profile for profile in report["profiles"] if profile["name"] == "synthetic-codex")
            self.assertEqual(synthetic["env_keys"], ["OPENAI_BASE_URL", "OPENAI_API_KEY"])
            self.assertEqual(synthetic["env_refs"], ["SYNTHETIC_API_KEY"])
            self.assertNotIn("sk-secret", str(report))

    def test_apply_openai_profile_preserves_unrelated_local_keys(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".kitsoki.yaml").write_text("story_dirs: [./stories]\n", encoding="utf-8")
            (root / ".kitsoki.local.yaml").write_text(
                """root:
  overrides:
    world:
      ticket_repo: me/project
harness_profiles:
  old-profile:
    backend: claude
    model: sonnet
""",
                encoding="utf-8",
            )
            report = apply_candidate(root, {
                "action": "upsert_openai",
                "name": "synthetic-codex",
                "backend": "codex",
                "model": "hf:test/model",
                "models": ["hf:test/model"],
                "env_var": "SYNTHETIC_API_KEY",
                "base_url": "https://api.synthetic.new/openai/v1",
            })
            text = (root / ".kitsoki.local.yaml").read_text(encoding="utf-8")
            self.assertEqual(report["status"], "applied")
            self.assertIn("ticket_repo: me/project", text)
            self.assertIn("old-profile:", text)
            self.assertIn("default_profile: synthetic-codex", text)
            self.assertIn('OPENAI_API_KEY: "${SYNTHETIC_API_KEY}"', text)

    def test_apply_replaces_existing_profile_block(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".kitsoki.local.yaml").write_text(
                """harness_profiles:
  synthetic-codex:
    backend: codex
    model: old
  keep-me:
    backend: claude
    model: opus
""",
                encoding="utf-8",
            )
            apply_candidate(root, {
                "action": "upsert_openai",
                "name": "synthetic-codex",
                "backend": "codex",
                "model": "new-model",
                "models": ["new-model"],
                "env_var": "OPENAI_API_KEY",
            })
            text = (root / ".kitsoki.local.yaml").read_text(encoding="utf-8")
            self.assertIn("model: new-model", text)
            self.assertNotIn("model: old", text)
            self.assertIn("keep-me:", text)

    def test_discover_reports_missing_referenced_env_as_not_ready(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".kitsoki.local.yaml").write_text(
                """default_profile: openai-codex
harness_profiles:
  openai-codex:
    backend: codex
    model: gpt-5.5
    env:
      OPENAI_API_KEY: "${KITSOKI_PROFILE_TEST_MISSING_KEY}"
""",
                encoding="utf-8",
            )
            old = os.environ.pop("KITSOKI_PROFILE_TEST_MISSING_KEY", None)
            try:
                report = discover(root)
            finally:
                if old is not None:
                    os.environ["KITSOKI_PROFILE_TEST_MISSING_KEY"] = old
            profile = next(profile for profile in report["profiles"] if profile["name"] == "openai-codex")
            self.assertEqual(profile["readiness"], "env-missing")

    def test_apply_rejects_raw_secret_instead_of_env_var_name(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            with self.assertRaises(ValueError):
                apply_candidate(root, {
                    "action": "upsert_openai",
                    "name": "bad",
                    "backend": "codex",
                    "model": "gpt-5.5",
                    "env_var": "sk-raw-secret",
                })


if __name__ == "__main__":
    unittest.main()
