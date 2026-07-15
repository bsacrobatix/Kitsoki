# implementation-handoff/v0

Required fields:

| Field | Meaning |
|---|---|
| `schema` | Must be `implementation-handoff/v0`. |
| `changeset` | Graph changeset id or applied changeset reference. |
| `target_repo` | Repository path or canonical repo id that will receive implementation work. |
| `acceptance` | One or more concrete acceptance criteria. |
| `sanctioned_command` | The non-executed command an operator may run to launch the target-repo Capsule implementation agent. |

Optional fields:

| Field | Meaning |
|---|---|
| `context` | Additional graph, docs, trace, or artifact paths. |
| `notes` | Human-readable constraints or reviewer guidance. |

The record is a handoff artifact, not an execution receipt. Tools that generate
it must not launch agents or mutate the target repository.
