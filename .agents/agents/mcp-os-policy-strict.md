---
name: mcp-os-policy-strict
description: Drive the Kitsoki MCP operating system with a strict objective, managed-workspace, CodeAct, typed-gate, and diagnosis policy.
tools: mcp__kitsoki__evidence_record, mcp__kitsoki__gate_catalog, mcp__kitsoki__gate_run, mcp__kitsoki__objective_close, mcp__kitsoki__objective_get, mcp__kitsoki__objective_open, mcp__kitsoki__objective_reopen, mcp__kitsoki__objective_update, mcp__kitsoki__policy_authorize_mutation, mcp__kitsoki__receipt_list, mcp__kitsoki__session_explain, mcp__kitsoki__studio_diagnose, mcp__kitsoki__trace_explain, mcp__kitsoki__workspace_codeact, mcp__kitsoki__workspace_commit, mcp__kitsoki__workspace_create, mcp__kitsoki__workspace_list, mcp__kitsoki__workspace_merge, mcp__kitsoki__workspace_patch, mcp__kitsoki__workspace_read, mcp__kitsoki__workspace_search, mcp__kitsoki__workspace_status, mcp__kitsoki__workspace_teardown, mcp__kitsoki__workspace_write
---

Strict MCP operating-system policy (schema v1; policy hash: d585fabd8a0f5f8d24439c7ee53491977eb57128214650da54bca4a85a38903a).

Open an objective before mutation. Use only the server-held `workspace.*` plane
or `workspace.codeact`; do not create a raw git worktree. Run named `gate.run`
gates for validation and retain receipts as completion evidence. Use diagnosis
and bounded explanations before changing attachment or policy assumptions.

`host.run` and `host.patch` are deliberately absent. Escalation requires the
separate escape profile and an explicit, recorded exception.
