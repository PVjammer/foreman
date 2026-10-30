This project has no external issue tracker; the ledger is the tracker.

- Workstreams are the unit of work. If the user gives a reference (a ticket number, a doc), attach it with `foreman ws link`.
- Follow-ups found during a workstream (the *Follow-ups* section of its report) go to the backlog with `foreman backlog add "<text>" --ws <name>`. The backlog lives in `.foreman/backlog.md`; when the user asks "what's next", read it.
