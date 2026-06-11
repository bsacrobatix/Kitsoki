#!/usr/bin/env python3
"""publish_proposal.py — move a drafted proposal out of the per-session
workspace into the docs/proposals/ queue, then mint a feature ticket that
links back to it so the proposal can be driven into implementation.

Usage:
    python3 publish_proposal.py <workspace> <slug> [change_target] [title] [idea]

  workspace      docs/proposals/.workspace/<slug> — holds 005-proposal.md
                 (the draft) plus the numbered check artifacts 001..004.
  slug           the meaningful slug minted at intake; the final filename
                 prefers the draft's own title heading, falling back to this.
  change_target  when set, the author AMENDED this existing proposal in
                 place instead of writing a new draft — nothing to move;
                 the existing path is reused as the published file.
  title          human-readable proposal title (from the draft artifact);
                 used as the feature ticket title. Falls back to the slug.
  idea           the one-line idea captured at intake; seeds the ticket body.

stdout: a single JSON object so host.run parses it into `stdout_json` and the
draft room binds several world keys from one call:

    {"proposal_file": "docs/proposals/<slug>.md",
     "ticket_id":     "F-<ts>-<slug>",
     "ticket_path":   "issues/features/F-<ts>-<slug>.md",
     "ticket_title":  "<title>"}

exit 0 on success, non-zero on error.

The numbered check artifacts (001-brief … 004-references) are left in the
workspace as the per-proposal record, disambiguated by their 3-digit
lexical-sort prefix.
"""

import json
import os
import re
import sys
from datetime import datetime, timezone


MAX_SLUG_WORDS = 6


def slugify(text: str) -> str:
    first_line = text.strip().split("\n")[0]
    slug = re.sub(r"[^a-z0-9]+", "-", first_line.lower()).strip("-")
    slug = "-".join(slug.split("-")[:MAX_SLUG_WORDS])
    return slug or "proposal"


def title_from_draft(draft: str) -> str:
    """First markdown heading in the draft, stripping `#` markers."""
    for line in draft.splitlines():
        m = re.match(r"^#{1,6}\s+(.+)", line.strip())
        if m:
            return m.group(1).strip()
    return ""


def find_path(base_dir: str, slug: str) -> str:
    path = os.path.join(base_dir, f"{slug}.md")
    if not os.path.exists(path):
        return path
    for i in range(2, 100):
        path = os.path.join(base_dir, f"{slug}-{i}.md")
        if not os.path.exists(path):
            return path
    raise RuntimeError("too many conflicts for slug: " + slug)


def write_feature_ticket(slug: str, title: str, idea: str, proposal_rel: str) -> tuple:
    """Mint issues/features/<id>.md linking back to the published proposal.

    Returns (ticket_id, ticket_rel_path). The id is timestamp-prefixed
    (`F-<ISO>-<slug>`) so it sorts newest-first alongside bug ids, and is
    collision-proofed against the features dir.
    """
    features_dir = os.path.join(os.getcwd(), "issues", "features")
    os.makedirs(features_dir, exist_ok=True)

    now = datetime.now(timezone.utc)
    base_id = f"F-{now.strftime('%Y-%m-%dT%H%M%SZ')}-{slug}"
    dest = find_path(features_dir, base_id)
    ticket_id = os.path.splitext(os.path.basename(dest))[0]
    filed_at = now.strftime("%Y-%m-%dT%H:%M:%SZ")

    ticket_title = title.strip() or slug
    body_idea = idea.strip()

    content = (
        "---\n"
        f'title: "{ticket_title}"\n'
        "status: open\n"
        "severity: P2\n"
        'assignee: ""\n'
        f'url: "{proposal_rel}"\n'
        "component: proposal\n"
        f'filed_at: "{filed_at}"\n'
        f'proposal: "{proposal_rel}"\n'
        "---\n\n"
        f"# {ticket_title}\n\n"
        "Implement the accepted proposal:\n\n"
        f"[{proposal_rel}]({proposal_rel})\n\n"
        + (f"{body_idea}\n\n" if body_idea else "")
        + "## Source\n\n"
        "Filed automatically when the proposal was published. The linked\n"
        "proposal document carries the full Why / What changes / Impact spine —\n"
        "read it before starting implementation.\n"
    )

    with open(dest, "w") as f:
        f.write(content)

    return ticket_id, os.path.relpath(dest)


def main() -> None:
    if len(sys.argv) < 3:
        print(
            f"usage: {sys.argv[0]} <workspace> <slug> [change_target] [title] [idea]",
            file=sys.stderr,
        )
        sys.exit(1)

    workspace = sys.argv[1]
    slug_in = sys.argv[2]
    change_target = sys.argv[3] if len(sys.argv) > 3 else ""
    title_in = sys.argv[4] if len(sys.argv) > 4 else ""
    idea_in = sys.argv[5] if len(sys.argv) > 5 else ""

    if change_target.strip():
        # Amend path: the author edited an existing proposal in place. Nothing
        # to move — the existing file is the published one.
        proposal_rel = os.path.relpath(change_target.strip())
        title = title_in.strip() or slug_in
    else:
        src = os.path.join(workspace, "005-proposal.md")
        if not os.path.isfile(src):
            print(f"publish_proposal: no draft at {src}", file=sys.stderr)
            sys.exit(1)

        with open(src) as f:
            draft = f.read()

        base_dir = os.path.join(os.getcwd(), "docs", "proposals")
        os.makedirs(base_dir, exist_ok=True)

        draft_title = title_from_draft(draft)
        title = title_in.strip() or draft_title or slug_in
        slug = slugify(draft_title) if draft_title else slugify(slug_in)
        dest = find_path(base_dir, slug)

        # Move the draft into the queue; leave the numbered checks in the
        # workspace as the record.
        os.replace(src, dest)
        proposal_rel = os.path.relpath(dest)

    # Mint the feature ticket that links back to the published proposal, so the
    # draft room can route straight into the implementation pipeline.
    ticket_id, ticket_rel = write_feature_ticket(slug_in, title, idea_in, proposal_rel)

    print(
        json.dumps(
            {
                "proposal_file": proposal_rel,
                "ticket_id": ticket_id,
                "ticket_path": ticket_rel,
                "ticket_title": title,
            }
        ),
        end="",
    )


if __name__ == "__main__":
    main()
