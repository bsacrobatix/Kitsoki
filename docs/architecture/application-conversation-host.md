# Application Conversation Host

`host.application_conversation.ask` is the generic daemon-owned boundary for a
story that needs a persistent, application-scoped LLM conversation over a fixed
graph projection. It is deliberately narrower than `host.agent.ask`: the story
can supply only an opaque chat identity and a question.

## Story contract

The complete call shape is:

```yaml
hosts:
  - host.application_conversation.ask

# Inside an effect:
invoke: host.application_conversation.ask
with:
  chat_id: "{{ slots.chat_id }}"
  question: "{{ slots.question }}"
bind:
  answer: answer
  conversation_ref: conversation_ref
  turn_ref: turn_ref
  conversation_receipt: receipt
```

Both inputs must be strings. `chat_id` is a bounded opaque identity; it may not
be a path or URL. Any additional argument is rejected. In particular a story
cannot select an application, agent, graph, provider, harness profile, model,
tool, MCP server, working directory, command, path, URL, or persistence
location.

The output contains:

| Field | Type | Meaning |
|---|---|---|
| `answer` | string | The durable assistant response. |
| `conversation_ref` | string | Opaque app-scoped chat reference. |
| `turn_ref` | string | Opaque, stable turn reference. |
| `receipt` | object | Canonical bounded receipt containing identities and digests, never prompt or response text. |
| `replayed` | bool | True when a completed turn was returned without another provider call. |

The handler is unavailable outside `kitsoki daemon` and for caller
applications without an exact binding.

## Daemon configuration

Bindings live in `.kitsoki.yaml` or its machine-local override and are keyed by
the caller application ID:

```yaml
harness_profiles:
  application-production:
    backend: codex
    model: gpt-5.6
    effort: medium
    env:
      OPENAI_API_KEY: "${OPENAI_API_KEY}"

application_conversations:
  support-console:
    application: product-knowledge
    role: customer-answerer
    graph:
      catalog: catalog/product.yaml
      audience: public
      fields: [summary, url]
      max_nodes: 500
    provider: application-model
    profile: application-production
    bounds:
      max_question_bytes: 8192
      max_answer_bytes: 32768
      max_history_bytes: 131072
      max_history_entries: 40
      max_graph_bytes: 262144
```

`application` must identify exactly one discovered story. That target story
must declare:

- the named `role` as an agent with an inline `system_prompt`;
- the named `provider`;
- inline `app.context`.

The role must have no tools, toolbox, MCP configuration, working directory,
prompt path, context path, or inherited coding-agent default prompt. The bound
harness profile must use a process-backed agent. Profile backend/model/effort
and environment override the target provider's corresponding values; the
provider supplies fallbacks.

The graph catalog is operator configuration, not story input. It must be a
repository-relative path or a `pg:` catalog reference. `audience`, scalar
`fields`, `max_nodes`, and byte bounds fix the exact snapshot exposed to the
role.

## Persistence and restart

The provider reuses the daemon chat store for app/room/scoped transcripts and a
separate turn ledger on the same database. Both stores support SQLite and
Postgres.

A turn advances monotonically:

1. Persist the user message and its sequence.
2. Capture the bounded graph and run the isolated agent.
3. Persist the answer and graph digest as `answer_ready`.
4. Persist the assistant message and its sequence.
5. Store the canonical receipt and mark the turn `completed`.

On daemon startup, pending provider calls become `interrupted`. Retrying the
same question resumes the stable turn without duplicating its user message.
An `answer_ready` turn completes without rerunning the provider. Completed
turns are replayable from their stored answer and integrity-checked receipt.
A different question is rejected until an interrupted turn is retried.

The provider process has no caller-selected execution authority. The underlying
agent call is isolated from ambient editor, visual, plugin, Studio MCP, and
operator-ask surfaces, and receives a tool-free role plus inline graph/history
data only.
