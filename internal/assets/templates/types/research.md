---
description: Investigate or diagnose without changing anything (logs, Datadog, dev testing, reading code)
worktrees: false
read_only: true
check_in: before running anything that changes state, even on dev
---
## start
- Restate the question you are answering in one line at the top of your report.

## work
- Read, query and run diagnostics. Don't change code, configs or shared systems. Your output is findings with evidence (queries, file paths, log lines).

## closeout
- Docs: fold in the findings that will still be true next month, with how you found them, so nobody has to repeat the investigation.
- Follow-ups: anything that needs fixing becomes a follow-up (with a suggested type: bug, feature, spike).
