# Cross-repository governance declaration

Kitsoki is governed by its own managed Capsule, Capsule-CI, durable queue, and
protected-main machinery. It must not install another repository's conventions
pack over its richer native merge, synchronization, agent-policy, or staging
tools.

`.kitsoki/cross-repo-governance.json` is the narrow declaration consumed by
POG's cross-repository router. Version 1 fixes the supported path to:

1. create a managed workspace with `kitsoki capsule workspace create-script`;
2. commit it with `kitsoki capsule workspace commit`, where `--project` is the
   protected source repository root;
3. from that workspace, run:

   ```sh
   kitsoki capsule promote --current --pipeline change --target main --gate "make test"
   ```

The promotion command runs or reuses exact-SHA Capsule CI, publishes an
immutable receipt-bound candidate, and submits it to Kitsoki's own durable
queue. The declaration grants no arbitrary command, pipeline, target, gate, or
cross-repository queue authority: consumers must validate every fixed field and
fail closed on unknown schemas or values.

This contract changes routing only. It does not authorize a caller to start a
queue worker, waive tests, apply an override, or mutate protected `main`.
