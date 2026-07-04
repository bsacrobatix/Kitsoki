export const graphExamples = [
  storyExample({
    key: "bugfix-story",
    title: "Bugfix pipeline",
    badge: "7 rooms / 9 edges",
    family: "Story graphs",
    description:
      "Declared room graph for stories/bugfix/app.yaml. The main spine is intentionally simple, with checkpoint refinement loops and a test-failure backtrack.",
    graph: makeBugfixStoryGraph(),
  }),
  storyExample({
    key: "cherny-story",
    title: "Cherny loop",
    badge: "8 rooms / 12 edges",
    family: "Story graphs",
    description:
      "Declared room graph for stories/cherny-loop/app.yaml. This makes the autonomous maker-checker feedback loop the central shape instead of a side case.",
    graph: makeChernyStoryGraph(),
  }),
  storyExample({
    key: "gitops-story",
    title: "Git ops",
    badge: "14 rooms / 23 edges",
    family: "Story graphs",
    description:
      "Declared room graph for stories/git-ops/app.yaml. It shows a hub-and-spoke workflow with conflict loops, backtracking, and cleanup/undo branches.",
    graph: makeGitOpsStoryGraph(),
  }),
  storyExample({
    key: "devstory-story",
    title: "Dev-story hub",
    badge: "18 rooms / 32 edges",
    family: "Story graphs",
    description:
      "Declared high-level graph for stories/dev-story/app.yaml. The day-level hub composes imported stories and several local loops for design, incidents, deploys, docs, and ad-hoc work.",
    graph: makeDevStoryGraph(),
  }),
  storyExample({
    key: "cherny-feedback-flow",
    title: "Cherny feedback run",
    badge: "observed loop",
    family: "Transcript-backed runs",
    description:
      "Observed path based on stories/cherny-loop/flows/feedback_loops_back.yaml. It includes two maker iterations and three gate checks before achieved.",
    graph: makeChernyFeedbackRunGraph(),
  }),
  storyExample({
    key: "gitops-conflict-flow",
    title: "Git conflict second round",
    badge: "observed retry",
    family: "Transcript-backed runs",
    description:
      "Observed path based on stories/git-ops/flows/conflict_second_round.yaml. It demonstrates a conflict loop that re-enters conflict, then exits through rebase continuation.",
    graph: makeGitOpsConflictRunGraph(),
  }),
  storyExample({
    key: "bugfix-trace",
    title: "Slidey bugfix trace",
    badge: "tour cassette",
    family: "Transcript-backed runs",
    description:
      "Trace-shaped graph from the slidey-bugfix tour cassette, showing agent calls, host calls, and the terminal shipped event through the same renderer-neutral contract.",
    graph: makeBugfixTraceGraph(),
  }),
];

export const graphFamilies = ["All", ...Array.from(new Set(graphExamples.map((item) => item.family)))];

function storyExample(input) {
  return input;
}

function makeBugfixStoryGraph() {
  return graph({
    graph_id: "story:stories/bugfix/app.yaml#rooms",
    kind: "room-state-machine",
    cyclic: true,
    meta: {
      source_story: "stories/bugfix/app.yaml",
      basis: "Declared rooms plus checkpoint loops from room comments and flow fixtures.",
    },
    nodes: [
      room("bf.idle", "Bug intake", 0),
      room("bf.reproducing", "Reproduce", 1, { has_agent: true, phase: "reproducer" }),
      room("bf.proposing", "Propose", 2, { has_agent: true, phase: "proposer" }),
      room("bf.implementing", "Implement", 3, { has_agent: true, phase: "implementer" }),
      room("bf.testing", "Test", 4, { has_agent: true, phase: "test_author" }),
      room("bf.validating", "Validate", 5, { has_agent: true, phase: "validator" }),
      room("bf.done", "Done / ship", 6, { terminal: true }, "terminal"),
    ],
    edges: [
      e("bf.idle", "start", "bf.reproducing", 0),
      e("bf.reproducing", "accept", "bf.proposing", 1),
      e("bf.reproducing", "refine", "bf.reproducing", 2, "loop", "recycle: refine reproduction"),
      e("bf.proposing", "accept", "bf.implementing", 3),
      e("bf.proposing", "refine", "bf.proposing", 4, "loop", "recycle: refine proposal"),
      e("bf.implementing", "implemented", "bf.testing", 5),
      e("bf.testing", "tests_failed", "bf.implementing", 6, "backtrack", "recycle: tests failed"),
      e("bf.testing", "tests_passed", "bf.validating", 7),
      e("bf.validating", "validated", "bf.done", 8),
    ],
  });
}

function makeChernyStoryGraph() {
  return graph({
    graph_id: "story:stories/cherny-loop/app.yaml#rooms",
    kind: "room-state-machine",
    cyclic: true,
    meta: {
      source_story: "stories/cherny-loop/app.yaml",
      basis: "Declared autonomous maker-checker loop; feedback flow fixture confirms repeated loop_again arcs.",
      observed_flow: "stories/cherny-loop/flows/feedback_loops_back.yaml",
    },
    nodes: [
      room("cherny.configuring", "Configure goal", 0),
      room("cherny.baseline", "Baseline gate", 1, { phase: "red-before-green" }),
      room("cherny.iterating", "Maker iteration", 2, { has_agent: true, phase: "maker" }),
      room("cherny.gating", "Gate check", 3, { phase: "checker" }),
      room("cherny.orchestrator", "Orchestrator", 2, { has_agent: true, optional: true }),
      room("cherny.workers", "Parallel workers", 3, { has_agent: true, optional: true }),
      room("cherny.achieved", "Achieved", 4, { terminal: true }, "terminal"),
      room("cherny.exhausted", "Budget exhausted", 4, { terminal: true }, "terminal"),
    ],
    edges: [
      e("cherny.configuring", "launch", "cherny.baseline", 0),
      e("cherny.baseline", "proceed", "cherny.iterating", 1),
      e("cherny.baseline", "reconfigure", "cherny.configuring", 2, "backtrack", "recycle: adjust gate"),
      e("cherny.iterating", "check", "cherny.gating", 3),
      e("cherny.gating", "loop_again", "cherny.iterating", 4, "backtrack", "recycle: gate failed"),
      e("cherny.gating", "mark_achieved", "cherny.achieved", 5),
      e("cherny.gating", "mark_iter_exhausted", "cherny.exhausted", 6),
      e("cherny.gating", "mark_cost_exhausted", "cherny.exhausted", 7),
      e("cherny.configuring", "dispatch", "cherny.orchestrator", 8),
      e("cherny.orchestrator", "start_workers", "cherny.workers", 9),
      e("cherny.workers", "check_merging", "cherny.gating", 10),
      e("cherny.configuring", "retry", "cherny.configuring", 11, "loop", "recycle: retry setup"),
    ],
  });
}

function makeGitOpsStoryGraph() {
  return graph({
    graph_id: "story:stories/git-ops/app.yaml#rooms",
    kind: "room-state-machine",
    cyclic: true,
    meta: {
      source_story: "stories/git-ops/app.yaml",
      basis: "Hub-and-spoke graph from declared intents and rooms; conflict arcs are corroborated by conflict_second_round.yaml.",
      observed_flow: "stories/git-ops/flows/conflict_second_round.yaml",
    },
    nodes: [
      room("git.idle", "Detect context", 0),
      room("git.main_ops", "Main ops hub", 1),
      room("git.branch_ops", "Branch ops hub", 1),
      room("git.commit", "Commit", 2, { has_agent: true }),
      room("git.staging", "Stage changes", 2),
      room("git.rebase", "Rebase", 2),
      room("git.conflict", "Resolve conflict", 3, { has_agent: true }),
      room("git.merge_branch", "Merge branch", 2),
      room("git.merge_into_main", "Merge into main", 2),
      room("git.worktree_create", "Create worktree", 2),
      room("git.worktree_list", "List worktrees", 2),
      room("git.cleanup", "Cleanup", 2),
      room("git.undo", "Undo", 2),
      room("git.done", "Done", 3, { terminal: true }, "terminal"),
    ],
    edges: [
      e("git.idle", "on_main", "git.main_ops", 0),
      e("git.idle", "on_branch", "git.branch_ops", 1),
      e("git.main_ops", "worktree_create", "git.worktree_create", 2),
      e("git.main_ops", "worktree_list", "git.worktree_list", 3),
      e("git.main_ops", "merge_branch", "git.merge_branch", 4),
      e("git.main_ops", "cleanup", "git.cleanup", 5),
      e("git.main_ops", "undo", "git.undo", 6),
      e("git.branch_ops", "commit", "git.commit", 7),
      e("git.branch_ops", "stage", "git.staging", 8),
      e("git.branch_ops", "rebase", "git.rebase", 9),
      e("git.branch_ops", "merge_into_main", "git.merge_into_main", 10),
      e("git.rebase", "rebase_conflict", "git.conflict", 11),
      e("git.conflict", "guide", "git.conflict", 12, "loop", "recycle: retry guidance"),
      e("git.conflict", "rebase_continue", "git.rebase", 13, "backtrack", "recycle: continue rebase"),
      e("git.conflict", "abort_to_branch", "git.branch_ops", 14, "backtrack", "recycle: abort to branch"),
      e("git.staging", "back", "git.branch_ops", 15, "backtrack", "recycle: back to branch"),
      e("git.commit", "nothing_staged", "git.staging", 16, "backtrack", "recycle: stage first"),
      e("git.commit", "accept", "git.branch_ops", 17, "backtrack", "recycle: commit done"),
      e("git.merge_into_main", "integrated", "git.done", 18),
      e("git.merge_branch", "conflict", "git.conflict", 19),
      e("git.cleanup", "cleanup_done", "git.main_ops", 20, "backtrack", "recycle: cleanup done"),
      e("git.worktree_create", "done", "git.branch_ops", 21),
      e("git.undo", "rollback", "git.branch_ops", 22, "backtrack", "recycle: rollback done"),
    ],
  });
}

function makeDevStoryGraph() {
  return graph({
    graph_id: "story:stories/dev-story/app.yaml#imports-and-hub",
    kind: "composed-room-state-machine",
    cyclic: true,
    meta: {
      source_story: "stories/dev-story/app.yaml",
      basis: "Top-level rooms and import aliases from the dev-story hub.",
    },
    nodes: [
      room("dev.main", "Main hub", 0),
      room("dev.inbox", "Inbox", 1),
      room("dev.ticket_search", "Ticket search", 1),
      room("dev.gitops", "Git ops import", 1, { import_alias: "gitops" }),
      room("dev.bf", "Bugfix import", 2, { import_alias: "bf", has_agent: true }),
      room("dev.pr", "PR refinement import", 3, { import_alias: "pr", has_agent: true }),
      room("dev.impl", "Implementation import", 2, { import_alias: "impl", has_agent: true }),
      room("dev.rev", "Code review import", 2, { import_alias: "rev", has_agent: true }),
      room("dev.prd", "PRD import", 2, { import_alias: "prd", has_agent: true }),
      room("dev.design_intake", "Design intake", 1),
      room("dev.design_refine", "Design refine", 2, { has_agent: true }),
      room("dev.design_draft", "Design draft", 3, { has_agent: true }),
      room("dev.incident", "Incident loop", 1),
      room("dev.deploy", "Deploy loop", 1),
      room("dev.observability", "Observability loop", 1),
      room("dev.docs", "Docs loop", 1),
      room("dev.agent", "Ask agent", 1, { has_agent: true }),
      room("dev.done", "Done", 4, { terminal: true }, "terminal"),
    ],
    edges: [
      e("dev.main", "go_inbox", "dev.inbox", 0),
      e("dev.inbox", "pick_ticket", "dev.bf", 1),
      e("dev.inbox", "pick_review", "dev.rev", 2),
      e("dev.main", "go_ticket_search", "dev.ticket_search", 3),
      e("dev.ticket_search", "pick_ticket", "dev.bf", 4),
      e("dev.main", "go_gitops", "dev.gitops", 5),
      e("dev.main", "go_bugfix", "dev.bf", 6),
      e("dev.bf", "bugfix_open_pr", "dev.pr", 7),
      e("dev.bf", "bugfix_leave_worktree", "dev.main", 8, "backtrack", "recycle: leave worktree"),
      e("dev.pr", "merged", "dev.main", 9, "backtrack", "recycle: PR merged"),
      e("dev.main", "go_implementation", "dev.impl", 10),
      e("dev.impl", "done", "dev.main", 11, "backtrack", "recycle: implementation done"),
      e("dev.main", "go_code_review_story", "dev.rev", 12),
      e("dev.rev", "done", "dev.main", 13, "backtrack", "recycle: review done"),
      e("dev.main", "go_prd", "dev.prd", 14),
      e("dev.prd", "prd_published", "dev.design_intake", 15),
      e("dev.main", "go_idea", "dev.design_intake", 16),
      e("dev.design_intake", "clarify", "dev.design_intake", 17, "loop", "recycle: clarify brief"),
      e("dev.design_intake", "ready", "dev.design_refine", 18),
      e("dev.design_refine", "refine", "dev.design_refine", 19, "loop", "recycle: refine design"),
      e("dev.design_refine", "accept", "dev.design_draft", 20),
      e("dev.design_draft", "published", "dev.main", 21, "backtrack", "recycle: design published"),
      e("dev.main", "go_incident", "dev.incident", 22),
      e("dev.incident", "watch", "dev.incident", 23, "loop", "recycle: keep watching"),
      e("dev.main", "go_deploy", "dev.deploy", 24),
      e("dev.deploy", "deploy_unhealthy", "dev.deploy", 25, "loop", "recycle: deploy unhealthy"),
      e("dev.main", "go_observability", "dev.observability", 26),
      e("dev.observability", "annotate_signal", "dev.observability", 27, "loop", "recycle: annotate signal"),
      e("dev.main", "go_docs", "dev.docs", 28),
      e("dev.docs", "revise_doc", "dev.docs", 29, "loop", "recycle: revise docs"),
      e("dev.main", "go_agent", "dev.agent", 30),
      e("dev.agent", "go_back", "dev.main", 31, "backtrack", "recycle: back to hub"),
    ],
  });
}

function makeChernyFeedbackRunGraph() {
  return graph({
    graph_id: "run:stories/cherny-loop/flows/feedback_loops_back.yaml",
    kind: "flow-transcript",
    cyclic: true,
    meta: {
      source_flow: "stories/cherny-loop/flows/feedback_loops_back.yaml",
      basis: "Mock observed event graph from the flow fixture host calls: gate-0, maker-1, gate-1, maker-2, gate-2.",
      expected_terminal: "__exit__achieved",
    },
    nodes: [
      event("cherny.run.configure", "operator", "Configure goal", 0, "user"),
      event("cherny.run.gate0", "host", "gate-0 fails burst", 1, "failed"),
      event("cherny.run.maker1", "agent", "maker-1 sizes bucket", 2, "passed", { has_agent: true }),
      event("cherny.run.artifact1", "host", "persist iteration-1", 3, "passed"),
      event("cherny.run.gate1", "host", "gate-1 fails refill", 4, "failed"),
      event("cherny.run.maker2", "agent", "maker-2 refills tokens", 5, "passed", { has_agent: true }),
      event("cherny.run.artifact2", "host", "persist iteration-2", 6, "passed"),
      event("cherny.run.gate2", "host", "gate-2 passes", 7, "passed"),
      event("cherny.run.achieved", "machine", "exit achieved", 8, "terminal"),
    ],
    edges: [
      observed("cherny.run.configure", "cherny.run.gate0", "launch", 0),
      observed("cherny.run.gate0", "cherny.run.maker1", "gate failed: start maker", 1),
      observed("cherny.run.maker1", "cherny.run.artifact1", "record", 2),
      observed("cherny.run.artifact1", "cherny.run.gate1", "check", 3),
      observed("cherny.run.gate1", "cherny.run.maker1", "feedback: failed gate", 4, "backtrack"),
      observed("cherny.run.gate1", "cherny.run.maker2", "recycle: next maker", 5, "backtrack"),
      observed("cherny.run.maker2", "cherny.run.artifact2", "record", 6),
      observed("cherny.run.artifact2", "cherny.run.gate2", "check", 7),
      observed("cherny.run.gate2", "cherny.run.achieved", "mark_achieved", 8),
    ],
  });
}

function makeGitOpsConflictRunGraph() {
  return graph({
    graph_id: "run:stories/git-ops/flows/conflict_second_round.yaml",
    kind: "flow-transcript",
    cyclic: true,
    meta: {
      source_flow: "stories/git-ops/flows/conflict_second_round.yaml",
      basis: "Observed turn graph from fixture turns and host calls.",
      expected_terminal: "branch_ops",
    },
    nodes: [
      event("git.run.initial", "machine", "initial conflict", 0, "active"),
      event("git.run.guide1", "operator", "guide: take ours", 1, "user"),
      event("git.run.conflict2", "machine", "re-enter conflict", 2, "active"),
      event("git.run.scan", "host", "gather conflict files", 3, "passed"),
      event("git.run.agent", "agent", "resolve second round", 4, "passed", { has_agent: true }),
      event("git.run.rebase", "host", "rebase continue", 5, "passed"),
      event("git.run.build", "host", "build check", 6, "passed"),
      event("git.run.branch", "machine", "return branch ops", 7, "terminal"),
    ],
    edges: [
      observed("git.run.initial", "git.run.guide1", "guide", 0),
      observed("git.run.guide1", "git.run.conflict2", "recycle: conflict again", 1, "backtrack"),
      observed("git.run.conflict2", "git.run.scan", "conflict_ready", 2),
      observed("git.run.scan", "git.run.agent", "host.agent.task", 3),
      observed("git.run.agent", "git.run.rebase", "rebase_continue_cmd", 4),
      observed("git.run.rebase", "git.run.build", "build_check_after_rebase", 5),
      observed("git.run.build", "git.run.branch", "rebase_done", 6),
    ],
  });
}

function makeBugfixTraceGraph() {
  return graph({
    graph_id: "trace:slidey-bugfix-tour#agent-and-host-calls",
    kind: "run-trace",
    cyclic: false,
    meta: {
      source_cassette: "stories/slidey-bugfix/cassettes/tour.cassette.yaml",
      story: "slidey-bugfix",
      ticket: "slidey-128",
    },
    nodes: [
      event("event:start", "operator", "Start slidey-128", 0, "user"),
      event("event:reproduce-agent", "agent", "Agent reproduces drift", 1, "passed", { has_agent: true }),
      event("event:propose-agent", "agent", "Agent proposes fallback fix", 2, "passed", { has_agent: true }),
      event("event:implement-agent", "agent", "Agent edits timing.js", 3, "passed", { has_agent: true }),
      event("event:run-tests", "host", "node --test timing", 4, "passed"),
      event("event:validate-agent", "agent", "Agent validates suite", 5, "passed", { has_agent: true }),
      event("event:shipped", "machine", "Reached shipped", 6, "terminal"),
    ],
    edges: [
      observed("event:start", "event:reproduce-agent", "start", 0),
      observed("event:reproduce-agent", "event:propose-agent", "bug_verified", 1),
      observed("event:propose-agent", "event:implement-agent", "accept_proposal", 2),
      observed("event:implement-agent", "event:run-tests", "implemented", 3),
      observed("event:run-tests", "event:validate-agent", "tests_passed", 4),
      observed("event:validate-agent", "event:shipped", "validated", 5),
    ],
  });
}

function graph({ graph_id, kind, cyclic, meta, nodes, edges }) {
  return {
    schema: "kitsoki.graph/v1",
    graph_id,
    kind,
    directed: true,
    cyclic,
    layout_hints: { default: "layered", rankdir: "LR" },
    meta,
    groups: nodes.map((node) => ({
      id: node.group,
      kind: node.kind === "state" ? "room" : "turn",
      label: node.label,
    })),
    nodes,
    edges,
  };
}

function room(id, label, distance, attrs = {}, status = "") {
  return {
    id: `state:${id}`,
    kind: "state",
    label,
    group: `room:${id}`,
    status,
    ref: { kind: "state", ref: id },
    attrs: { distance, source_ref: id, ...attrs },
  };
}

function event(id, kind, label, distance, status, attrs = {}) {
  return {
    id,
    kind,
    label,
    group: `turn:${distance}`,
    status,
    ref: { kind: "trace_event", ref: id.replace(/^.*:/, "") },
    attrs: { distance, ...attrs },
  };
}

function e(sourceRoom, intent, targetRoom, index, route = "forward", label = intent) {
  const handles = handlesForRoute(route);
  return {
    id: `transition:${sourceRoom}:${intent}:${targetRoom}:${index}`,
    kind: "transition",
    source: `state:${sourceRoom}`,
    target: `state:${targetRoom}`,
    label,
    status: route === "forward" ? "" : route,
    attrs: {
      intent,
      route,
      source_ref: sourceRoom,
      target_ref: targetRoom,
      transition_index: index,
      ...handles,
    },
  };
}

function observed(source, target, label, index, route = "forward") {
  const handles = handlesForRoute(route);
  return {
    id: `observed:${index}:${source}->${target}`,
    kind: "observed-transition",
    source,
    target,
    label,
    status: route === "forward" ? "active" : route,
    attrs: { sequence: index, intent: label, route, ...handles },
  };
}

function handlesForRoute(route) {
  if (route === "loop") {
    return { source_handle: "source-bottom", target_handle: "target-top" };
  }
  if (route === "backtrack") {
    return { source_handle: "source-bottom", target_handle: "target-bottom" };
  }
  return { source_handle: "source-right", target_handle: "target-left" };
}
