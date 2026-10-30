This project tracks work in **Linear**. Use whatever Linear access is available to you (a Linear MCP server or CLI); check which exists before relying on it. If neither is available, fall back to the ledger: `foreman backlog add` and tell the user issues need creating.

- Every workstream should map to a Linear issue. Pass `--ticket ID` to `foreman ws new` (or `foreman ws link` later).
- When work has no issue, ask the user whether to create one; if yes, create it from the goal, then link it.
- Follow-ups in a worker's report: propose them as new issues; create only the ones the user approves, and backlog the rest with `foreman backlog add`.
- Never change issue state, assignee or comments without the user asking.
