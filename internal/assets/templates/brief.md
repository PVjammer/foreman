# Brief: {{.M.Name}}

- **Type:** {{.M.Kind}} ({{.T.Description}}){{if .M.Ticket}}
- **Ticket:** {{.M.Ticket}}{{end}}
- **Project root:** {{.Root}}
- **Working directory:** {{.M.Cwd}}{{if .M.Worktrees}}
- **Worktrees** (your own branches; commit here, never in the main checkouts):{{range .M.Worktrees}}
  - `{{.Repo}}` → `{{.Path}}` on branch `{{.Branch}}`{{end}}{{end}}

## Goal

{{.M.Goal}}

## Before you start
{{with .T.Phase "start"}}
{{.}}
{{end}}
- Project docs (decisions, how things work, gotchas from earlier workstreams) are in `{{.Docs}}`, if it exists.

## How to work
{{with .T.Phase "work"}}
{{.}}
{{end}}{{if .T.ReadOnly}}
- This workstream is **read-only**: don't change code, configs or shared systems.{{end}}{{if .T.CheckIn}}
- **Check in** {{.T.CheckIn}}: write your report with `status: needs-input` describing exactly what you want to do, and wait.{{end}}
- Messages from foreman start with `[foreman → <recipient>]`. If one is addressed to anyone other than `{{.M.Name}}`, it reached you by mistake: don't act on it; say so in one line and stop.
- Stay inside the goal. If you find other problems, list them under *Follow-ups*; don't fix them.
- If the goal is ambiguous or you need a decision, don't guess: write the report with `status: needs-input` and stop.

{{if .Landing}}## Landing

{{template "landing-steps" .Landing}}
{{end}}## Report

Write your report to `{{.Report}}` when you finish or need input, replacing any earlier version. Use exactly this shape:

```markdown
---
status: done | needs-input | failed
---
## Summary
One to three lines a manager could read.

## Changes
What changed and where (repo, branch, commits), or "none".

## Decisions
Choices you made that someone should know about.

## Open questions
What you need from the user (required when status is needs-input).

## Follow-ups
Problems or ideas found along the way that deserve their own ticket.
```

Keep it short; the orchestrator reads reports instead of your transcript.
