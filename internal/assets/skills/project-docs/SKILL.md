---
name: project-docs
description: Seed or reorganize a foreman project's docs folder (foreman-docs/ by default), where closeouts fold in decisions, how things work, gotchas and runbooks. Use when foreman says the docs folder doesn't exist yet, or when the user asks to set up, restructure or tidy the project docs. Not for recording what a single workstream learned (its closeout does that). Orchestrator only.
---

# Project docs

Orchestrator only. If you are a workstream worker, this isn't for you: stop.

The docs folder holds what workstreams learned: decisions and why, how things work, gotchas, runbooks. Its path is in `.foreman/ORCHESTRATOR.md` (and `foreman doctor` prints it). Closeouts fold knowledge in, so you rarely write there yourself.

## Seeding a missing folder

Offer it; don't do it unasked.

1. Read the root `AGENTS.md` / `CLAUDE.md` and each repo's README.
2. Propose a short outline to the user: a `README.md` index plus a few pages, for example `architecture.md`, `deploy.md`, `gotchas.md`. Say in a line what each will hold.
3. Write it only once the user agrees. Keep pages short. **Link to READMEs; don't copy them.** Start pages that have no content yet with a one-line "what goes here", not invented content.

## Maintaining it

When the user asks to tidy or restructure:

- Keep `README.md` as the index: every page listed with one line on what it covers.
- Merge overlapping pages, split pages that cover several unrelated topics, remove session diaries and stale content (ask before deleting).
- Prefer a few well-named pages over many small ones.
- If the change is large, propose it first, or start a workstream for it (ws-start skill, type `research` or `feature`).

Where the folder lives (committed, local, external) is a setting chosen at `foreman init`; don't move it.
