---
description: Fix a bug: reproduce, fix with a regression test, tracked
worktrees: true
check_in: before pushing, opening a PR, or changing anything outside your worktrees (your report excepted)
---
## start
- Follow `.foreman/tracker.md`: read the ticket if there is one; note if one should be created.
- Reproduce the bug first and record how in your report. If you can't reproduce it, stop with `status: needs-input`.

## work
- Fix the root cause, not the symptom, and add a regression test where the repo has tests. Commit on your branch.

## closeout
- Ticket: propose a comment with root cause, fix and how it was verified, plus the state transition. Never change ticket state without approval.
- Docs: record the root cause and anything a future debugger should know (a troubleshooting page is a good home).
