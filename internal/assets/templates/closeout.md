[foreman → {{.M.Name}}] Time to close out workstream "{{.M.Name}}". Don't do more work on the goal.

Write {{.Closeout}} in exactly this shape, then stop and wait for approval. Don't apply anything yet.

---
status: proposed
---
## Final report
The outcome in a few lines: what was done, what wasn't, and where it lives.

## Doc updates
Changes to the project docs folder ({{.Docs}}). For each one, a `### <path relative to that folder>` heading, then exactly what to add or change. Prefer editing existing pages over adding new ones. Only durable knowledge: decisions and why, how things work, gotchas. Not a diary of this session. Write "none" if nothing is worth keeping.

## Tracker updates
Comments or state changes to propose (tracker: {{.Tracker}}, see .foreman/tracker.md), or "none".

## Follow-ups
One line each, with a suggested workstream type (feature, bug, research, spike, poc, deploy).

## Branches and worktrees
Each branch: committed or not, ready to merge or not, and anything uncommitted. Nobody merges for you at closeout, so say exactly what the user should merge, into what, and in which order.
{{with .T.Phase "closeout"}}
For this type ({{$.T.Name}}):
{{.}}
{{end}}
