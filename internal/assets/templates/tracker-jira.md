This project tracks work in **Jira**. Use whatever Jira access is available to you (an Atlassian MCP server, or the `jira` CLI); check which exists before relying on it. If neither is available, fall back to the ledger: `foreman backlog add` and tell the user tickets need creating.

- Every workstream should map to a Jira issue. When the user starts work that has an issue, pass `--ticket KEY` to `foreman ws new` (or `foreman ws link` later).
- When work has no issue, ask the user whether to create one; if yes, create it with a summary taken from the goal, then link it.
- Follow-ups in a worker's report: propose them to the user as new issues (one line each). Create only the ones the user approves; put the rest in `foreman backlog add`.
- Never transition, reassign or comment on issues without the user asking.
