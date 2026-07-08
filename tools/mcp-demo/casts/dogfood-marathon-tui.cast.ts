/**
 * Synthetic TUI replay for the dogfood-marathon story. The issue references are
 * real, captured from GitHub/local artifacts on 2026-07-08, but this committed
 * cast is still a no-LLM replay: it demonstrates the restart/journal/deck UX
 * without spending model tokens during render or test.
 */
import { type Termcast, ansi as a } from "./types.js";

interface BugRef {
  id: string;
  source: string;
  title: string;
  url: string;
  result: string;
  cost: string;
  time: string;
}

const bugs: BugRef[] = [
  { id: "CF#66", source: "constructorfabric", title: "tui: orchestrator AskOffPath conversation lane error", url: "https://github.com/constructorfabric/Kitsoki/issues/66", result: "exception", cost: "$0.42", time: "6m" },
  { id: "CF#65", source: "constructorfabric", title: "tui: bug report 2026-07-07T08:02:50Z", url: "https://github.com/constructorfabric/Kitsoki/issues/65", result: "partial", cost: "$0.37", time: "5m" },
  { id: "CF#64", source: "constructorfabric", title: "tui: dev-story landing room quick actions wrapping awkwardly", url: "https://github.com/constructorfabric/Kitsoki/issues/64", result: "fixed", cost: "$0.51", time: "8m" },
  { id: "CF#63", source: "constructorfabric", title: "tui: inbox showing multiple times in TUI", url: "https://github.com/constructorfabric/Kitsoki/issues/63", result: "fixed", cost: "$0.49", time: "7m" },
  { id: "CF#62", source: "constructorfabric", title: "tui: bug report 2026-07-07T07:19:24Z", url: "https://github.com/constructorfabric/Kitsoki/issues/62", result: "partial", cost: "$0.32", time: "4m" },
  { id: "CF#61", source: "constructorfabric", title: "First-run local harness/profile setup", url: "https://github.com/constructorfabric/Kitsoki/issues/61", result: "fixed", cost: "$0.56", time: "9m" },
  { id: "CF#60", source: "constructorfabric", title: "bugfix gave no intermediate status; execution unclear", url: "https://github.com/constructorfabric/Kitsoki/issues/60", result: "finding", cost: "$0.28", time: "4m" },
  { id: "BS#1202", source: "bsacrobatix", title: "bug file-findings silently drops status=blocked findings", url: "https://github.com/bsacrobatix/Kitsoki/issues/1202", result: "fixed", cost: "$0.47", time: "7m" },
  { id: "BS#1201", source: "bsacrobatix", title: "scenario-qa live drive requires undocumented outer harness=live", url: "https://github.com/bsacrobatix/Kitsoki/issues/1201", result: "finding", cost: "$0.21", time: "3m" },
  { id: "BS#1200", source: "bsacrobatix", title: "scenario-qa bugfix x tui leg cannot provision a ticket", url: "https://github.com/bsacrobatix/Kitsoki/issues/1200", result: "partial", cost: "$0.39", time: "6m" },
  { id: "BS#1199", source: "bsacrobatix", title: "gh-agent incident: github:bsacrobatix/Kitsoki/issue/1196", url: "https://github.com/bsacrobatix/Kitsoki/issues/1199", result: "exception", cost: "$0.18", time: "2m" },
  { id: "BS#1198", source: "bsacrobatix", title: "web report-bug modal network trace rows hide JSON-RPC method", url: "https://github.com/bsacrobatix/Kitsoki/issues/1198", result: "fixed", cost: "$0.44", time: "6m" },
  { id: "BS#1197", source: "bsacrobatix", title: "web trace user-subsystem rows render empty event label", url: "https://github.com/bsacrobatix/Kitsoki/issues/1197", result: "fixed", cost: "$0.41", time: "5m" },
  { id: "BS#1196", source: "bsacrobatix", title: "web tour step 12 Observe mode popup occludes anchor", url: "https://github.com/bsacrobatix/Kitsoki/issues/1196", result: "partial", cost: "$0.52", time: "8m" },
  { id: "LOCAL#063431", source: "local", title: "bugfix live run blocked: codex profile dispatches unauthenticated Claude Code agent", url: ".artifacts/issues/bugs/2026-07-08T063431Z-bugfix-live-run-blocked-codex-profile-dispatches-unauthentic.md", result: "exception", cost: "$0.09", time: "2m" },
];

function clip(text: string, max = 60): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

function shell(line: string): string {
  return `${a.gray("$")} ${line}\n`;
}

function operatorInput(text: string): string {
  return `${a.cyan("You")} ${a.gray("compose")} ${text}\n`;
}

function tuiHeading(title: string): string {
  return `${a.gray("┌─")} ${a.bold("kitsoki")} ${a.gray("────────────────────────────────────────────────────────────")}\n` +
    `${a.gray("│")}  ${a.cyan("dogfood-marathon")} ${a.gray("·")} ${a.bold(title)}\n` +
    `${a.gray("└────────────────────────────────────────────────────────────")}\n`;
}

const captionPad = "\n\n\n";

function bugRows(): string {
  return bugs.map((b, i) =>
    `${String(i + 1).padStart(2, "0")}  ${a.cyan(b.id.padEnd(11))} ${a.gray(b.source.padEnd(17))} ${clip(b.title, 64)}\n` +
    `    ${a.gray(b.url)}\n`,
  ).join("");
}

function progressRows(): string {
  return bugs.map((b, i) => {
    const style = b.result === "fixed" ? a.green : (b.result === "exception" ? a.yellow : a.cyan);
    return `${String(i + 1).padStart(2, "0")} ${a.cyan(b.id.padEnd(11))} triage ${a.green("STILL-LIVE")}  drive ${style(b.result.padEnd(9))}  ${b.cost.padStart(5)}  ${b.time.padStart(3)}  deck ${a.gray(`decks/${b.id.toLowerCase().replace("#", "-")}.slidey.json`)}\n`;
  }).join("");
}

function deckRows(): string {
  return bugs.map((b) => `- ${b.id}: .artifacts/dogfood-marathon/bug15/decks/${b.id.toLowerCase().replace("#", "-")}.slidey.json\n`).join("");
}

export const cast: Termcast = {
  agent: "dogfood-marathon-tui",
  title: "kitsoki tui — dogfood marathon bug15",
  curtainTitle: "Kitsoki TUI  ·  dogfood marathon bug15",
  footer: "kitsoki · TUI tour · dogfood-marathon over 15 real bug references",
  badge: "TUI",
  assertText: ["dogfood-marathon", "cases       15", "cf#66", "bs#1202", "local#063431"],
  cols: 118,
  rows: 32,
  beats: [
    {
      id: "start",
      label: "Start durable marathon",
      caption: "Launch kitsoki cleanly, then ask the TUI to run the marathon",
      sub: "one continuous operator session · no launch arguments",
      holdMs: 4200,
      chunks: [
        { kind: "out", data: shell("kitsoki tui") },
        { kind: "out", data: tuiHeading("idle") },
        { kind: "out", data: `${a.bold("Ready.")} What should kitsoki work on?\n\n` },
        { kind: "type", data: "Run a dogfood marathon over constructorfabric/Kitsoki, bsacrobatix/Kitsoki, and my local bug reports. Use the codex-native GPT-5.5 profile, limit it to 15 bugs, keep a durable journal, and only stop for serious questions." },
        { kind: "out", data: `\n\n${operatorInput("Run a dogfood marathon over constructorfabric/Kitsoki, bsacrobatix/Kitsoki, and my local bug reports.")}\n` },
      ],
    },
    {
      id: "intake",
      label: "Load 15 bugs",
      caption: "The story loads 15 real bug references and writes the first journal checkpoint",
      sub: "7 constructorfabric · 7 bsacrobatix · 1 local artifact",
      holdMs: 6200,
      chunks: [
        { kind: "out", data: tuiHeading("intake") + captionPad },
        { kind: "out", data: `${a.bold("Story")}    dogfood-marathon\n${a.bold("Model")}    codex-native · GPT-5.5\n${a.bold("Journal")}  .artifacts/dogfood-marathon/bug15/journal.json\n${a.bold("Limit")}    15\n${a.bold("Loaded")}   ${a.green("15 bugs")}\n\n` },
        { kind: "out", data: bugRows() },
      ],
    },
    {
      id: "resume",
      label: "Resume-safe checkpoint",
      caption: "The same TUI session shows the restart-safe journal without a second invocation",
      sub: "progress is recorded after intake and every bug",
      holdMs: 4400,
      chunks: [
        { kind: "out", data: tuiHeading("processing") + captionPad },
        { kind: "out", data: `${a.bold("Checkpoint")} intake ${a.gray("→")} journal written\n${a.bold("Journal")}    .artifacts/dogfood-marathon/bug15/journal.json\n${a.bold("Restart safe")} ${a.green("yes")}  next bug: 1 of 15  processed: 0\n${a.bold("Next")} continue autonomously unless a serious question is raised\n\n` },
      ],
    },
    {
      id: "speedrun",
      label: "Sped-up 15-bug run",
      caption: "Sped-up TUI pass: each bug is triaged, driven, verified, journaled, and decked",
      sub: "serious questions pause; otherwise the loop advances",
      holdMs: 7600,
      chunks: [
        { kind: "out", data: tuiHeading("processing · sped up") + captionPad },
        { kind: "out", data: `${a.gray("idx case        triage       drive       cost  time  artifact")}\n` },
        { kind: "out", data: progressRows() },
      ],
    },
    {
      id: "exception",
      label: "Serious questions surface",
      caption: "The story raises serious exceptions to the operator instead of hiding them inside an agent prompt",
      sub: "operator-visible checkpoint, durable journal sidecar",
      holdMs: 5200,
      chunks: [
        { kind: "out", data: tuiHeading("exception review") + captionPad },
        { kind: "out", data: `${a.yellow("LOCAL#063431")}  bugfix live run blocked: codex profile dispatches unauthenticated Claude Code agent\n` },
        { kind: "out", data: `${a.bold("Question")} codex-native requested, but the dispatch path attempted unauthenticated Claude Code.\n${a.bold("Journal")}  .artifacts/dogfood-marathon/bug15/journal.md\n\n` },
        { kind: "type", data: "Record that as an operator checkpoint and continue the marathon report." },
        { kind: "out", data: `\n\n${operatorInput("Record that as an operator checkpoint and continue the marathon report.")}${a.green("acknowledged")} exception recorded; continuing from the saved checkpoint at bug 15\n\n` },
      ],
    },
    {
      id: "report",
      label: "Deck artifacts",
      caption: "The report step writes one aggregate Slidey deck plus a deck for every bug",
      sub: "review artifacts are stable under .artifacts/dogfood-marathon/bug15",
      holdMs: 6400,
      chunks: [
        { kind: "out", data: tuiHeading("reporting") + captionPad },
        { kind: "out", data: `${a.bold("Aggregate")} .artifacts/dogfood-marathon/bug15/decks/aggregate.slidey.json\n${a.bold("Per-bug decks")} 15\n\n` },
        { kind: "out", data: deckRows() },
      ],
    },
    {
      id: "done",
      label: "Rollup",
      caption: "Run rollup: time, cost, fixed count, partials, exceptions, and findings",
      sub: "the TUI ends on the same durable journal and report handles",
      holdMs: 6200,
      chunks: [
        { kind: "out", data: tuiHeading("done") + captionPad },
        {
          kind: "out",
          data:
            `${a.bold("Cases")}       15\n` +
            `${a.bold("Fixed")}       ${a.green("6")} verified or directly fixed in the replay artifact set\n` +
            `${a.bold("Partial")}     ${a.cyan("5")} need follow-up verification\n` +
            `${a.bold("Exceptions")}  ${a.yellow("3")} serious operator checkpoints\n` +
            `${a.bold("Findings")}    2 story hardening findings\n` +
            `${a.bold("Time")}        82m wall-clock equivalent\n` +
            `${a.bold("Cost")}        $5.66 recorded model-cost equivalent\n` +
            `${a.bold("Report")}      .artifacts/dogfood-marathon/bug15/decks/aggregate.slidey.json\n\n` +
            `${a.green("done")} dogfood marathon journal, per-bug decks, aggregate deck, and TUI video ready for QA\n`,
        },
      ],
    },
  ],
};
