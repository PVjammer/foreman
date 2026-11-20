---
name: foreman-troubleshoot
description: Troubleshoot foreman itself when something misbehaves. Covers a `foreman` command failing, a worker gone or stuck, notices that don't arrive or are misrouted, and the sidebar or watcher not updating. Runs `foreman doctor`, checks logs and workstream state, and reports observations rather than guesses. Use when foreman, herdr or a worker session isn't behaving as expected. Orchestrator only.
---

# Troubleshoot foreman

Orchestrator only. If you are a workstream worker, this isn't for you: stop.

## 1. Look

```bash
foreman doctor                 # herdr, integrations, profiles, summarizer, watcher, settings
foreman ws list                # does foreman think the workstream is open, and in what state?
foreman ws show <name>         # meta, status and report
foreman ws peek <name> -n 80   # the worker's terminal, when the report doesn't explain it
```

Logs live in `.foreman/logs/`: `watch.log` (watcher: bootstrap, events, delivery, closeout).

## 2. Common cases

| Symptom | What to try |
|---|---|
| Worker gone (no agent, pane missing) | `foreman ws resume <name>` restarts it from its brief and report. Or `foreman ws close <name>` if the user is done with it. Ask the user which. |
| Worker stuck on a dialog | That's for the user to answer (inbox), not you. |
| `foreman ws …` fails inside a Claude session (sandbox, permission prompts) | `foreman doctor` flags it; the user runs `foreman setup-claude` once and restarts Claude. |
| No notices, sidebar not updating | `foreman doctor` → watcher running? plugin linked? Check `watch.log`. The user can restart with `foreman up`. |
| A message addressed to someone else reached you | Don't act on it. Tell the user "foreman sent me a message meant for <recipient>" and stop. |

Don't fix it by doing the work yourself (writing a worker's closeout, editing its files). That hides the bug and mixes contexts.

## 3. Report observations, not guesses

Tell the user:

- **What you ran** (the command) and **what it printed** (relevant lines, trimmed).
- **What you expected** instead.
- Anything relevant from `doctor` or the log.

If you have an explanation, label it as a guess ("Guess: the watcher may not have restarted after…"). Don't present a guess about the cause as a finding. If the user wants it tracked, `foreman backlog add "<observation>"`.
