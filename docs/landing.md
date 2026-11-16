# Landing

*Optional. A project without `[landing] enabled = true` closes workstreams out as before (`ws finish` → approve). Decision: [design.md, D11](design.md#d11-workstreams-end-by-landing-opt-in-2026-11-16).*

A workstream **lands** when its work is merged. The worker prepares that before it asks anything; you decide with one press; foreman does the mechanics. The worker's session ends as soon as the work is ready, so a PR that sits in review for days costs no session.

```
working ──done + packet──▶ ready ──Land──▶ in-review ──merged on the forge──▶ landed ▶ archived
   ▲                         │  ╲              │
   │                         │   ╲─ff────────────────────────────────────────▶ landed ▶ archived
   └── keep working / ◀──────┘    conflict or failing gate: back to a worker
       handback                   PR closed unmerged ──▶ unmerged ──▶ keep working | park | close
```

## What the worker does

Its brief has a **Landing** section (only in landing projects). When the goal is done it:

1. rebases onto the base branch and runs the tests,
2. commits everything, **doc edits included, on its branch**, so they go through review with the code (foreman-docs in `local`/`external` mode go in `docs.patch` instead, applied by foreman once landed),
3. writes `landing.md` (title, description, tests, summary: it becomes the PR),
4. drafts follow-ups in `followups.jsonl` (`{"title","goal","type","mode"}` per line),
5. sets its report to `done` and stops.

The watcher sees `done`, checks the packet (complete, nothing uncommitted), **ends the worker's session**, and posts one **🛬 ready to land** item to the inbox with a toast, plus a one-line notice to the orchestrator.

## What you do

In the inbox (`prefix+i`) on a ready item:

| Key | |
|---|---|
| `L` | Land |
| `1`–`9` | cycle that follow-up: queue → start → start (autonomous) → drop |
| `W` | keep working: type what to change (e.g. paste review comments) |
| `P` | park: stays open and idle, no nudges, branch and PR untouched |
| `C` | close without landing (branch kept) |
| `g` | the diff against the base, in git's pager |

Or tell the orchestrator, which runs `foreman ws land <name> [--start 1] [--drop 2]`, `ws keep`, `ws park`, `ws close`.

## What foreman does at Land

Everything is checked for every repo before anything changes. A missing token, a dirty main checkout or a network failure stops Land with nothing done.

- **ff (personal projects):** the main checkout must be clean and on the base; foreman runs the configured `gate` in the worktree, checks the branch fast-forwards, and `git merge --ff-only`s it. Then it applies the docs patch, files follow-ups, closes and archives.
- **pr (forge projects):** foreman fetches the base (from `upstream` for forks) and checks for conflicts without touching a checkout, pushes the branch with **your** git credentials (workers never get push access or a token), finds an open PR for the branch or opens one, and files follow-ups. The workstream is then **in review**: open, idle, never nudged. The watcher polls the forge (`poll_minutes`, default 10). When the forge says **merged** (squash and rebase merges included: state comes from the API, never from git ancestry), foreman applies the docs patch and archives. A PR **closed without merging** comes back to the inbox: keep working, park or close; never silently.

A conflict, a branch that can't fast-forward, a failing gate or uncommitted work goes **back to a worker** on the same branch with the details (the session restarts, natively resumed where the harness supports it). It reports done again and you get a new ready item. **Keep working** works the same way with your note, and the next Land pushes to the same open PR.

**Follow-ups** are filed at Land into `.foreman/proposals.jsonl` (append-only, linked to the source workstream). *Start* ones go to the orchestrator, which writes the brief and runs `ws new`; *queued* ones wait in `foreman proposals` until you `proposals start <id>`; *dropped* ones are recorded. Nothing a worker writes is ever executed: packet files are read without following symlinks, capped, stripped of control characters, and the docs patch is confined to the docs folder.

Merging through the forge API (Land = merge) is out of scope for now: on forges, someone else usually merges.

## Setting it up

`foreman init` asks "Use landing for this project?" (default no). `foreman landing setup` re-asks later. Per repo it detects:

- the forge and project from the remote URL (`github.com` → GitHub, a host with `gitlab` → GitLab, anything else is asked),
- a fork layout (`origin` + `upstream`: push to your fork, PR on upstream),
- the base branch, and a gate for ff mode (`go test ./...`, `npm test`, `make test`, pytest),

and asks for the mode (pr/ff/off), branch template (default `fm/{name}`; `{ticket}` works), draft or ready, and a **token command** per forge host (`gh auth token`, `pass show …`, `secret-tool lookup …`). The token is checked right away. A command beats an environment variable here: watchers are started by herdr and don't see your shell's variables. The forge entry can be saved for this project or globally.

```toml
[landing]
enabled = true
nudge_hours = 4            # one nudge when ready work waits this long (0 = never)
poll_minutes = 10

[landing.repos."."]
mode = "pr"                # pr (alias mr) | ff
base = "main"
remote = "origin"          # where foreman pushes (your fork, for forks)
upstream = "upstream"      # forks: where the base lives; default = remote
forge = "github.com"       # a key of [forges]
project = "upstream-org/lib"   # where the PR opens
head_owner = "nick"        # forks: owner of the pushed branch
branch = "fm/{name}"
draft = false

[landing.repos."site"]
mode = "ff"
base = "main"
gate = "make test"

[forges."github.com"]
kind = "github"
api = "https://api.github.com"
token_cmd = ["gh", "auth", "token"]

[forges."gitlab.example.com"]
kind = "gitlab"
api = "https://gitlab.example.com"     # path prefixes work: https://host/gitlab
token_cmd = ["pass", "show", "gitlab/api"]
```

`foreman doctor` and `foreman landing status` check each forge token (`/user`) and that the push remote answers `git ls-remote` without prompting (2FA, VPN, ssh agent). Problems are warnings: landing never blocks anything else.

A workstream can opt out with `ws new --no-landing`.

## Trying it

`scripts/landing-scenario.sh [dir]` runs the whole thing in a temp dir with the real CLI: local bare repos stand in for GitHub and GitLab (`url.insteadOf`), `cmd/fakeforge` answers the API, herdr is stubbed, and workers are simulated. It covers opt-out, ff with follow-ups and a docs patch, a handback when main moved, a GitHub PR with keep-working and a squash merge, a GitHub fork, and a self-hosted GitLab MR that is closed, reworked and merged. Nothing leaves the machine.

### On real GitHub (by hand)

1. Make a throwaway repo you own and clone it as a project root (or a repo inside one).
2. Have a token: `gh auth token` if you use gh, or a fine-grained PAT for that repo only (Contents: read/write, Pull requests: read/write) in your password manager.
3. `foreman init` (or `foreman landing setup`): landing yes, mode `pr`, your token command. It should print "authenticated as …".
4. `foreman doctor`: the landing lines should be ✔.
5. `foreman up` and ask the orchestrator for a tiny feature on that repo.
6. When the inbox shows 🛬 ready to land, check the preview, press `L`. The PR appears on GitHub.
7. Leave a review comment and relay it: `foreman ws keep <name> "<comment>"` (or ask the orchestrator; a PR in review has no inbox item). The worker continues on the branch; when it's ready again, press `L`: the same PR gets the new commit.
8. Squash-merge on GitHub. Within `poll_minutes` (or `foreman landing poll`) the workstream lands and is archived.
9. Variation: close a PR without merging and see the ↩ item.
