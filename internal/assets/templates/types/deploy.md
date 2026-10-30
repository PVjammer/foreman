---
description: Change a shared environment (deploys, helm, config); always checks in first
worktrees: false
check_in: before every command that changes a deployment or shared system
---
## start
- Follow `.foreman/tracker.md` for the ticket. Gather the current state (versions, values, health) before proposing anything.

## work
- Before any change, write your report with `status: needs-input` listing exactly what you will run and how to roll back, then wait for approval. After each change, verify health and record it.

## closeout
- Ticket: propose a comment with what was deployed where, verification, and rollback steps.
- Docs: update the runbook or deployment notes with anything that differed from the docs.
