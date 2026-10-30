---
description: Build a feature: code in worktrees, tracked from start to finish
worktrees: true
check_in: before pushing, opening a PR, or changing anything outside your worktrees (your report excepted)
---
## start
- Follow `.foreman/tracker.md`: if the brief names a ticket, read it; if there is none, say in your report that one should be created (don't create it yourself unless the tracker instructions say to).
- Read the project docs folder for anything relevant before designing the change.

## work
- Make the change in your worktree(s), with tests where the repo has them. Commit on your branch with clear messages.
- Don't push or open PRs unless the goal says to.

## closeout
- Ticket: propose a comment summarizing what shipped and the state transition (e.g. to review). Never change ticket state without approval.
- Docs: fold in decisions, new behaviour and gotchas a future session would need, for example in an architecture or how-to page.
- Branches: list each branch and whether it is ready to merge.
