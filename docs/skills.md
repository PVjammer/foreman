# Orchestrator skills

foreman ships a set of [Agent Skills](https://agentskills.io/specification) that teach the orchestrator its recurring procedures. `.foreman/ORCHESTRATOR.md` holds what must always be in context: the tools, the rules and the safety rules. The procedures load only when needed, and you can invoke them by name in the orchestrator session (`/ws-status` in Claude Code, `/skill:ws-status` in pi).

| Skill | Use it for |
|---|---|
| `ws-start` | Turning a request into a workstream: a ticket-quality goal, plus type, profile, repos and name, then `foreman ws new` (with `--dry-run` to preview). Also handles requests from the new-workstream box (`prefix+f`) and follow-ups you approved at landing. |
| `ws-status` | A status update for you or a manager over a period, built from `foreman digest`, outcomes first. |
| `ws-triage` | What needs attention now: inbox items, blocked or needs-input workers, finished-but-open and stale workstreams, with one suggested action each. |
| `ws-closeout` | Ending a finished workstream either way, acting only on your word. **Landing** projects ([landing.md](landing.md)): present a ready item, `ws land` with your follow-up choices, relay review comments with `ws keep`, `ws park`, handle a PR closed unmerged and queued proposals. **Closeout**: `ws finish`, present the draft, `ws approve`, then turn follow-ups into backlog items or new workstreams. |
| `project-docs` | Seeding the docs folder when it doesn't exist, or restructuring it. |
| `foreman-troubleshoot` | When foreman, herdr or a worker misbehaves: `foreman doctor`, logs, resume or close. It reports what it observed and labels any guess as a guess. |

Every skill starts with "Orchestrator only". Workers run under the project root, so their harness can discover the project's skills too, and the guard tells them to leave these alone.

## Installing

`foreman init` installs the skills for the harnesses of the orchestrator and scratch profiles (pass `--no-skills` to skip). For an existing project, or another harness:

```bash
foreman skills                       # list them, with install state per skill dir
foreman skills install               # orchestrator + scratch harnesses
foreman skills install --harness all # or claude, pi, opencode (comma-separated)
foreman skills install --dry-run     # show what would change
foreman skills show ws-start         # print one (for harnesses without skill support)
```

| Harness | Skill dir | Notes |
|---|---|---|
| Claude Code | `.claude/skills/` | Listed and invocable as `/ws-start` etc. |
| pi | `.agents/skills/` | Loaded only once you **trust the project** (pi asks on first start; `pi --approve` trusts it for one run). Invocable as `/skill:ws-start`. |
| opencode | `.agents/skills/` | Discovered; opencode also reads `.claude/skills/` and keeps one copy of each name. Invocation by name not tested. |
| others | none | Can read skills with `foreman skills show <name>`; ORCHESTRATOR.md says so. |

### Re-installing and editing

Re-running `install` is safe. foreman records the hash of every file it writes in `.foreman/skills.json`:

- A file identical to the shipped version is left alone.
- A file foreman wrote earlier and nobody changed is updated to the new version.
- **A file you edited is kept**, with a warning. `--force` replaces it and leaves your copy next to it as `SKILL.md.foreman-bak`.
- A skill foreman no longer ships is removed if untouched, and kept if edited.

So you can customize a skill in place; `foreman doctor` lists edited skills, and flags missing or outdated ones.

### Git

In docs mode `commit`, the skill dirs are ordinary project files: commit them so teammates' orchestrators get them. In `local` and `external` modes foreman adds them to `.git/info/exclude`, like `.foreman/`.

## Upgrading an existing project

`foreman skills install` adds the skills but doesn't touch your `ORCHESTRATOR.md`. The old guide still works, since it carries the procedures inline. To switch to the trimmed guide that points at the skills, run `foreman init --force` (it regenerates `ORCHESTRATOR.md` and `tracker.md`, so copy any edits you made first).

## Changing the skills

The source lives in `internal/assets/skills/<name>/SKILL.md` and is embedded in the binary, like the built-in types. Rules for editing:

- `name` must match the directory.
- Keep each description specific about **when** to use the skill and distinct from the others; descriptions are what the harness matches requests against.
- Keep the "Orchestrator only" guard.

`go test ./internal/skills` checks names, descriptions and the guard, and checks that `ORCHESTRATOR.md` still mentions every skill and keeps the safety rules.
