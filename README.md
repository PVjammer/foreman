# foreman

One orchestrator conversation per project. Feature work, fixes, investigations and deploys run as **workstreams** in other agent sessions. You hear about them through pop-ups and an inbox instead of switching tabs, and when a workstream is done it **closes out**: what it learned is folded into the project's docs and the session ends.

Built on [herdr](https://herdr.dev) (terminal multiplexer for coding agents) with no fork: foreman is a CLI plus a watcher, driven through herdr's CLI, a herdr plugin (event hooks), keybindings, sidebar metadata and toasts. Any herdr-supported harness (Claude Code, pi, OpenCode, Codex, …) can be the orchestrator or a worker.

> Status: prototype for finding the right *feel*. Three UX candidates are switchable live with `foreman mode`. Design decisions and their alternatives: [docs/design.md](docs/design.md#decisions-2026-10-29).

## Concepts

| Thing | What it is |
|---|---|
| **Project** | A directory with `.foreman/` (usually a root holding several repos, with an `AGENTS.md`/`CLAUDE.md`). Each project is a **space** (workspace) in one shared herdr session, so several projects sit side by side in one window, each with its own orchestrator and workstreams. |
| **Orchestrator** | The agent you talk to, in the space's first tab. Runs at the project root, follows `.foreman/ORCHESTRATOR.md`, and starts, reads and closes out workstreams through the `foreman` CLI. |
| **Workstream** | One task, one worker agent, in its own tab (layout `tabs`) or as a zoomed pane in a shared `workstreams` tab (layout `panes`). Has a **type** and a **profile**. |
| **Type** | What kind of work it is: `feature`, `bug`, `research`, `spike`, `poc`, `deploy`, or your own. A markdown file: frontmatter sets the mechanics (worktrees, read-only, when to check in, default profile), sections give the `start` / `work` / `closeout` instructions. |
| **Profile** | A named harness + args with a "use for" note, like `pi-small` for lookups and `claude` for features. The orchestrator picks one per task. |
| **Brief / report** | What crosses between orchestrator and worker. The brief (`brief.md`) goes in; the worker writes `report.md` with `status: done`, `needs-input` or `failed`. |
| **Closeout** | `foreman ws finish`: the worker drafts `closeout.md` (final report, doc updates, tracker updates, follow-ups). You approve it in the inbox, the worker applies it, and foreman closes the session, removes the worktrees (branches stay) and archives the workstream. |
| **Docs folder** | Where closeouts fold knowledge in (`foreman-docs/` by default). Committed with the project, kept local (git-excluded), or stored outside it, chosen at `foreman init`. |
| **Ledger** | `.foreman/`: per-workstream `meta.json`, `status.json`, `report.md`, `closeout.md`, plus append-only `events.jsonl`, `inbox.jsonl` and `acks.jsonl`. Closed workstreams move to `archive/`. One writer per file, so no locks. |
| **Watcher** | One background process per open project. Woken by foreman's herdr plugin on every agent state change (with a slow fallback poll), it writes a local-model summary into the sidebar, delivers events according to the mode, drives closeout, and nudges about stale workstreams. The plugin's hooks start it and restart it. |
| **Tracker** | `ledger` (local backlog), `jira` or `linear`. Instructions in `.foreman/tracker.md` for the orchestrator and workers to use whatever MCP server or CLI they have. |

## Hotkeys and views

Every session lives in its own tab. A **view** is a live window onto one: **floating** (a popup) or **docked** (a split beside the orchestrator). Closing a view with `ctrl+b q` leaves the session running. Keys (prefix = `ctrl+b`) are set in `[keys]` and chosen to avoid herdr's own:

| Key | Does |
|---|---|
| `prefix+m` | Switch to the orchestrator pane; press again to go back where you were |
| `prefix+M` (`shift+m`) | The orchestrator as a floating window |
| `prefix+f` | New workstream: a text box sent to the orchestrator, which writes the brief and picks type, profile and repos. If it's busy, the request waits and is delivered when it's free. `ctrl+d` in the box starts it directly as typed. |
| `prefix+a` | **Scratch**: one per project, for quick throwaway questions with the project's context. Floating; created on first use. `prefix+A` clears it. |
| `prefix+t` | **Terminal**: a persistent shell at the project root, floating |
| `prefix+d` | Dock a session (scratch, terminal or any workstream) beside the orchestrator; choosing a docked one undocks it. Docks come back after a restart. |
| `prefix+i` | Inbox. Enter on a blocked worker or a question opens its live terminal so you answer the real dialog (`ctrl+b q` returns); `v` docks it |
| `prefix+I` (`shift+i`) | Jump to the workstream that most needs you |

The same things work from the command line: `foreman orch`, `foreman view <orchestrator|scratch|term|name>`, `foreman dock [name]`, `foreman undock <name>`, `foreman request "…"`, `foreman scratch clear`.

## The three candidates (`foreman mode <name>`)

1. **notifier**: Toasts with summaries; you switch to the workstream yourself (`prefix+shift+i` jumps to the one that most needs you).
2. **inbox** (default): Toasts point you to a popup inbox (`prefix+i`). From it you can read the report, closeout or terminal, reply, press keys in a dialog, close out (`f`), approve a closeout (`a`), hand the item to the orchestrator, or dismiss it, all without leaving the orchestrator.
3. **relay**: Events are injected into the orchestrator conversation as `[foreman] …` lines once it is idle. You answer through it. Blocked prompts still toast.

## Setup

```bash
make install                         # binary → ~/.local/bin, then `foreman install` (below)
herdr integration install claude     # optional, per harness: lets herdr resume conversations natively
cd ~/work/my-project && foreman init # asks: name, docs location, tracker, orchestrator/worker/scratch profiles
foreman doctor
foreman up                           # opens herdr; quitting stops it, the next `up` (or plain `herdr`) resumes
```

`foreman install` sets up the machine once (re-running it is safe):
- `setup-herdr`: foreman's keybindings and sidebar rows in herdr's config, and foreman's herdr plugin.
- `setup-claude`, if Claude Code is installed: adds `foreman` to `sandbox.excludedCommands`, herdr's socket to `sandbox.network.allowUnixSockets`, and `Bash(foreman:*)` to `permissions.allow` in `~/.claude/settings.json`. Without these, a Claude orchestrator can read the ledger but every `foreman ws …` fails inside Claude's sandbox, or stalls on permission prompts.
- Reloads a running herdr's config.

Both setup steps merge into your existing files, keep a `.foreman-bak` backup, and can be previewed with `--print`. Claude reads its settings at startup, so restart any Claude sessions that were already running (in a pane: `/exit`, then `claude --continue`).

`foreman init` detects repos (up to three levels deep), writes `.foreman/config.toml`, `ORCHESTRATOR.md` and `tracker.md`, and on first use a commented `~/.config/foreman/config.toml`. Pass flags (`--docs-mode local --tracker jira --yes`) to skip the questions.

`setup-herdr` merges into your existing herdr config: it keeps your toast `delivery` choice, skips tables you already define, validates the result with `herdr config check`, and leaves a `.foreman-bak` backup. It also generates and links foreman's herdr plugin, whose hooks start each project's watcher when herdr starts and wake it the moment an agent changes state. Keybindings come from `[keys]` and avoid herdr's own; `doctor` flags clashes.

## Configuration

Layers, each overriding the one before: built-in defaults → `~/.config/foreman/config.toml` → `.foreman/config.toml` → the workstream's type → `--profile` / `--type` flags.

```toml
[workers]
profile = "claude"          # default worker
type = "feature"            # default type

[profiles.pi-small]         # add your own; claude, pi and opencode exist built in
harness = "pi"
args = ["--model", "qwen3.8-27b"]
use_for = "small, well-scoped tasks: lookups, mechanical edits, running tests"

[profiles.pi-boxed]         # a worker inside a sandbox wrapper: see docs/harness-wrapper.md
harness = "pi"
wrapper = ["agentbox"]      # runs `agentbox pi …`; fails closed, never falls back to bare pi
use_for = "work on security-sensitive repos"

[keys]                      # herdr keybindings (prefix = ctrl+b); re-run setup-herdr after changes
inbox = "prefix+i"
next = "prefix+shift+i"
orchestrator = "prefix+m"
orchestrator_float = "prefix+shift+m"
scratch = "prefix+a"
scratch_clear = "prefix+shift+a"
terminal = "prefix+t"
dock = "prefix+d"
new = "prefix+f"

[scratch]
profile = "claude"          # asked at `foreman init`

[ui]
mode = "inbox"              # notifier | inbox | relay
layout = "tabs"             # tabs | panes
stop_on_exit = true

[docs]
dir = "foreman-docs"
mode = "commit"             # commit | local | external

[hygiene]
stale_days = 3              # suggest closing out after this long...
max_turns = 40              # ...or this many turns
max_open = 8                # warn past this many open workstreams
```

Behaviour lives in text you can edit: `.foreman/ORCHESTRATOR.md`, `.foreman/tracker.md`, and types (`foreman types copy feature` copies one into `.foreman/types/` to customize; `--as datadog` makes a new type, `--global` puts it in `~/.config/foreman/types/` for every project).

## Commands

```
foreman up [--keep] [--no-attach]    open the project's space (starts herdr if needed)
foreman down                         close this project's space; state stays, `up` brings it back
foreman ws new <name> --goal "…" [--type feature] [--profile pi-small] [--repos a,b] [--ticket KEY]
foreman ws finish <name>             close out: worker drafts closeout.md for approval
foreman ws approve <name> [--note …] worker applies it; foreman closes and archives
foreman ws list [--all] | show | peek | send | keys | link | close | resume | focus
foreman inbox [--list]               popup TUI (bound to prefix+i) or plain list
foreman next                         jump to the workstream that most needs you (prefix+shift+i)
foreman digest [--since 24h|7d]      roll-up the orchestrator turns into status updates
foreman backlog add "…" [--ws name]
foreman types [show|copy] · foreman profiles
foreman mode [notifier|inbox|relay] · foreman layout [tabs|panes] · foreman tracker [ledger|jira|linear]
foreman orch · view <target> · dock [target] · undock <target> · request ["…"] · scratch clear
foreman init · setup-herdr · doctor · watch
```

## Trying it

`scripts/scenario.sh setup [dir]` builds a toy two-repo project (pi on a local model for orchestrator and workers). Run it twice with different dirs and `NAME=` to get two projects side by side. Then `foreman up` in one, and `scripts/scenario.sh start` from another terminal. One workstream finishes, one has an ambiguous goal, and one uses Claude so you hit a dialog. Try `foreman ws finish` on a finished one to see closeout.

To test without touching your own herdr or foreman setup, point `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `XDG_STATE_HOME` at a scratch directory: herdr and foreman both follow them, so you get a separate herdr (default session included) and a separate project registry.

See [docs/design.md](docs/design.md) for the reasoning, decisions, findings and open questions.
