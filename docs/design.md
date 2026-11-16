# foreman: design notes

*Started 2026-10-28. This prototype exists to find the right feel, not to implement a spec.*

## Problem

Day-to-day work on one project with many facets (a backend repo, an info site, a helm deployment, docs and scripts) means running several agent sessions at once: features, bug fixes, deployment coordination, Datadog investigations, testing against dev. The two current options both fail:

- **Many tabs:** too much sprawl on one laptop screen.
- **One tab, switching Claude sessions:** you miss when something is blocked or done.

Either way, **no session has the global project context**, and producing a status update for management means reconstructing the day by hand.

## Shape of the solution

- **Talk to one session per project.** The orchestrator delegates; workers do the work.
- **Worker interactions surface to you** (toasts, inbox), not by switching sessions.
- **The orchestrator is not in the event loop.** Events go from workers to the UI to you, mechanically. Routing every event through the orchestrator would bloat its context within a day. It learns about work from **reports and the digest**, never from transcripts.
- **Workstreams are the unit, not sessions.** A workstream maps to a ticket, has a kind (build / investigate / deploy), and owns its worktrees.
- **The ledger is the backbone.** It provides global context for the orchestrator, recovery after restarts, and the raw material for status updates (`foreman digest`).
- **Thin host, behaviour in text.** Go code is mechanical: herdr calls, files, git. What agents do is set by `ORCHESTRATOR.md`, the brief and closeout templates, workstream types and `tracker.md`, which are editable per project.
- **Workstreams end.** A finished workstream closes out: its knowledge goes into the project docs, its session ends. Neither the number of sessions nor any one session's context grows without bound.

## Why herdr, and why not a fork

herdr already provides PTY sessions, agent state detection (idle / working / blocked / done) for about 20 harnesses, a socket and CLI API, and native session resume through per-harness integrations. Everything foreman needed beyond that turned out to be reachable without a fork:

| Need | herdr feature used |
|---|---|
| Inbox UI | `[[keys.command]] type = "popup"`: a modal terminal on a hotkey |
| Summaries in the sidebar | `pane report-metadata --token summary=…` plus `[ui.sidebar.agents] rows = [..., ["$summary"]]` |
| Pop-ups | `notification show` (in-app toasts with sounds; `delivery = "terminal"` for desktop notifications) |
| One window, several projects | One session (herdr's `default`); a project is a workspace labelled with its name; workstreams are labelled tabs (or panes, with `pane zoom`) |
| Per-project counts in the sidebar | `workspace report-metadata --token inbox=…` plus `[ui.sidebar.spaces] rows` |
| End on close, resume on open | `server stop` on quit. On the next start herdr restores the layout and natively resumes agents that have integrations. foreman restarts any others from brief + report. |
| Reopening plain `herdr` restarts foreman | foreman's herdr plugin: a `[[startup]]` hook starts watchers for open projects; event hooks wake (or start) them on every agent state change. (Before the plugin: a `tab_bar_right` command entry, which herdr runs on the server every few seconds.) |
| Isolated testing | herdr follows `XDG_CONFIG_HOME` (config, sessions, sockets); `HERDR_CONFIG_PATH` points at an alternate config |
| Change detection | Polling `agent list`; `state_change_seq` and `completion_seq` make transitions unambiguous |

Not available: opening a popup programmatically (only `popup.close` is exposed), so toasts point at the hotkey.

## Lifecycle

`foreman up` does the following:

1. Start the shared herdr server if it isn't running, and make sure the project's workspace exists (labelled with the project name; its first tab is `orchestrator`).
2. Start the project's watcher as a detached process (one per project, guarded by a lock file).
3. From a plain terminal, attach the herdr UI; from inside herdr, just focus the project's space.
4. When you quit, the server stops (unless `--keep` is passed), which ends every open project.

The watcher bootstraps once per herdr server lifetime. It recognizes a new lifetime by the orchestrator pane's terminal id, which herdr changes on every restart.

- If the orchestrator came back natively, it is told to rehydrate from `foreman digest`. Otherwise a fresh orchestrator is started and pointed at `ORCHESTRATOR.md`. Either way it is told to offer seeding the docs folder if that doesn't exist.
- Workers that came back natively but were mid-task get a nudge that their last step was interrupted. Workers that didn't come back are restarted with a "resuming" kickoff from brief + report.
- Prompts to agents that aren't ready yet are held and sent once the agent is idle.

The watcher exits when its project's workspace closes (`foreman down`) or the server goes away. When herdr reopens, the plugin's startup hook starts watchers for every registered project with an open space; its event hooks wake a watcher on every agent state change, or start one if none is running.

Landing (opt-in, D11): the worker prepares a packet and reports done → the watcher marks it ready, ends its session, posts one inbox item → Land: foreman fast-forwards, or pushes and opens a PR/MR → the watcher polls the forge and archives once merged. See [landing.md](landing.md).

Closeout (projects without landing): `ws finish` → the worker writes `closeout.md` (`status: proposed`) → the watcher posts it to the inbox → `ws approve` (or `a` in the inbox) → the worker applies it and sets `status: applied` → the watcher closes the workstream: removes worktrees (only caches may be left behind; real changes block it), closes the pane, archives the folder, clears its inbox items.

## Findings from the first scenario run (2026-10-28)

The run used pi on a local Qwen3.8-27B for the orchestrator and two workers, Claude for one worker, and gemma4 for summaries.

- **The end-to-end loop works.** Brief → work in a worktree → report → event → summary → inbox item, for all three workstreams. The local-model orchestrator wrote a usable manager update from `foreman digest`, though it invented a time ("this morning"). The status-update instructions need "don't state times you don't have".
- **Dialogs have different defaults, so a generic approve key is unsafe.** Claude's folder-trust dialog highlights "No, exit"; pressing Enter killed the worker. The inbox now offers `y` (Enter on the highlighted option, with the dialog shown in the preview), `1`–`9`, `s` (arbitrary keys, such as `down enter`) and `x` (Esc). Per-harness and per-dialog answer recipes are a likely next step.
- **First prompts can't assume the agent is ready.** Claude came up behind the trust dialog, so `agent start` returned `agent_not_ready`. Kickoffs are now parked in `kickoff.txt` and delivered by the watcher once the agent is idle; the dialog surfaces as a normal `blocked` event. On a real project whose root is already trusted, this should mostly go away.
- **Ambiguous goals don't reliably produce questions.** "Update the tagline to something better" was simply done. That's acceptable, but if you want check-ins, the brief template needs a firmer instruction.
- **The orchestrator's first-turn picture goes stale** when workstreams are started outside it (the scenario script). In normal use the orchestrator starts them, so this matters less, but relay mode or a "what changed since your last turn" note would fix it.

## Decisions (2026-10-29)

Made after the first scenario run. Each one is listed with the options we passed over, so it can be revisited.

### D1. One herdr session for all projects; a project is a space

- **Chosen:** one shared herdr session: herdr's `default`, so `foreman up` and plain `herdr` open the same place. Each project is a herdr **workspace** (a "space" in the sidebar), labelled with the project name. Tab 1 is the orchestrator. Workers go in a tab each (`[ui] layout = "tabs"`, the default) or as panes of one `workstreams` tab, where focusing one zooms it (`layout = "panes"`). Any other tabs are yours, and foreman ignores them. Agent names are prefixed with the project (`acme-orch`) because herdr agent names are unique per session.
- **Why:** two projects can be open side by side in one window. The sidebar shows each worker by workstream name (a `$ws` pane token), whatever the layout, so workstreams don't depend on tabs.
- **Revised during the build:** the plan was zoomed panes in one tab. Measured on a 200×50 terminal, unzoomed panes are about 23×84 with four workers but 23×41 with eight, too narrow for Claude's or pi's TUI, and those hidden panes are where dialogs render and summaries are read from. The user's own AI-Projects run had 13 workers. So `tabs` is the default and `panes` is an option for a handful of workers. herdr can't hide the tab bar except with a single tab.
- **Alternatives:** one herdr session per project (the first prototype: projects couldn't share a window); a dedicated `foreman` session (plain `herdr` wouldn't reopen it); herdr's linked worktree workspaces (git-only, one repo, so they don't fit a multi-repo root).

### D2. Close stops everything, reopen resumes; `foreman down` ends one project

- **Chosen:** option C. Quitting herdr through foreman stops the server, so every project's agents end. The next `foreman up`, or plain `herdr`, restores every project that was open and restarts its watcher. `foreman down` ends one project mid-day; its state stays in the ledger and it stays closed until `foreman up`.
- **What "resume" means:** the layout always comes back. A conversation comes back only for harnesses with a herdr integration installed; other workers restart from brief + report. Work in progress is interrupted, not paused, and the worker is told so.
- **Alternatives:** A, quit stops everything with no per-project close; B, closing the window detaches and agents keep running (herdr's default) until `foreman down`. B means agents can spend tokens or sit blocked while you're away.

### D3. Layered config with named worker profiles

- **Chosen:** global `~/.config/foreman/config.toml`, overridden by project `.foreman/config.toml`, overridden by the workstream type, overridden by a per-workstream `--profile` / `--harness`. Profiles are named harness + args with a `use_for` description. The orchestrator guide lists the profiles, so the orchestrator picks one per task in text. `init` needs no flags.
- **Alternatives:** a single per-project harness setting (the first prototype); routing rules in code (rejected: thin host).

### D4. Workstream types are files

- **Chosen:** types are markdown files with frontmatter (mechanics: worktrees, profile, check-in rule) and phase sections (`start`, `work`, `closeout`). They're instruction text that foreman sends at the right moments. They're looked up in `.foreman/types/`, then `~/.config/foreman/types/`, then the built-ins (feature, bug, research, spike, poc, deploy). Tracker steps refer to `tracker.md`, so Jira, Linear and the ledger stay interchangeable.
- **Alternatives:** hardcoded kinds (the first prototype's build / investigate / deploy).

### D5. Closeout is a worker phase, approved by the user

- **Chosen:** `foreman ws finish <name>` sends the worker its type's closeout prompt. The worker writes `closeout.md`: final report, proposed doc edits (with a target file for each), tracker updates and follow-ups. It shows up in the inbox; on approval **the same worker applies the edits** and exits. Then foreman closes the pane, removes the worktrees (branches are kept) and moves the workstream to `.foreman/archive/`, where the digest still sees it. The watcher suggests closeout or a split for old or long workstreams.
- **Alternatives:** the orchestrator applies the edits (it has less context and more of its own to protect); no fold-in (the first prototype's `ws close`).

*Amended by D11: projects that turn landing on end workstreams by landing instead; this closeout stays the default.*

### D6. A docs folder per project, location chosen at init

- **Chosen:** `[docs] dir` (default `foreman-docs`) and `[docs] mode`:
  - `commit`: in the project, versioned with it.
  - `local`: in the project, but listed in `.git/info/exclude`, so it's never committed and teammates never see it.
  - `external`: under `~/.local/share/foreman/<project>/`, so the repo is untouched.

  `foreman init` asks interactively; flags skip the questions. On first `foreman up`, the orchestrator offers to seed the folder from existing READMEs and AGENTS.md (content needs judgment, so that's a skill; settings are mechanical, so that's `init`).
- **Alternatives:** always committed (ties foreman to this repo's layout); always outside the project.

### D7. Scratch session profile is configurable, asked at init (2026-10-29)

- **Chosen:** `[scratch] profile`, asked during `foreman init`, with Claude as the default.
- **Alternatives:** a local model by default (cheaper, but weaker answers).

### D8. The orchestrator always resumes (2026-10-29)

- **Chosen:** native resume, every time. A fresh start happens only when asked (`foreman orch refresh`: the orchestrator writes a handoff note, and a new one starts from it plus the digest).
- **Alternatives:** a fresh orchestrator each day, or on every start. Both avoid an ever-growing transcript but lose conversational continuity. Revisit if the orchestrator's context visibly degrades.

### D9. One view, two placements: floating or docked (2026-10-29)

- **Chosen:** any session (orchestrator, worker, scratch, terminal) can be **floated** (a popup attached to its live pane) or **docked** (a split next to the orchestrator, running the same attach). The session itself always stays in its own tab. A view is just a window onto it, so docking never moves panes or changes ids. Closing a view (`ctrl+b q`) leaves the session running.
- **Constraints:**
  - One writable owner per terminal. Floating a session that is docked jumps to the dock instead of taking it over.
  - Docks aren't restored by herdr after a restart (restored panes come back as shells). foreman records them and reopens them during bootstrap.
- **Alternatives:** `pane move` into the orchestrator's tab. It moves the real pane, which changes pane ids and empties the session's tab.

### D10. Hotkey to switch to the orchestrator (2026-10-29)

- **Chosen:** a key that focuses the current project's orchestrator pane. Pressed again, it returns to where you were. Separate from the key that floats the orchestrator.

### D11. Workstreams end by landing, opt-in (2026-11-16)

**Problem.** Closeout took four or five separate prompts (suggest finish → finish → approve → merge by hand → turn follow-ups into work), and merging wasn't one of them. Each step took about a minute, but done workstreams waited hours to days between them (the 13 AI-Projects reviews: about 2 days from done to finish; agentbox-foreman-mode: 7 hours), finished branches lingered (fm/harness-wrapper), and done workers kept long sessions alive.

**Chosen:** a workstream ends when its work **lands**; the session ends when the work is **ready**. Details in [landing.md](landing.md).

- **The worker prepares, then exits.** Its brief says: rebase, test, commit (doc edits on the branch, so they're reviewed), write `landing.md` (the PR text), draft follow-ups (`followups.jsonl`), docs-folder edits outside the repo as `docs.patch`. On `done` + a complete packet the watcher marks it **ready**, ends the session and posts **one** inbox item.
- **One decision:** Land / Keep working / Park / Close / Diff, with start / queue / drop (and a mode) per follow-up.
- **foreman does the mechanics** (thin host): checks every repo first, then fast-forwards (personal projects) or pushes with the user's git credentials and opens a PR/MR via the forge's REST API (GitHub, GitLab; no gh/glab needed). Workers never get push access or tokens.
- **Landed = the forge says merged** (polled; squash merges count; never git ancestry). In review the workstream is open, idle and never nudged, for days if need be. Closed unmerged → inbox: keep working, park or close; never silent.
- **Problems go back to a worker** on the same branch with the error: conflicts, a branch that can't fast-forward, a failing gate, uncommitted work. Keep working is the same with the user's note; the next Land updates the same open PR.
- **Follow-ups are filed by foreman** at Land into `proposals.jsonl` (append-only, one mechanism for interactive and autonomous work). Started ones go to the orchestrator, which writes the brief; queued ones wait for the user.
- **Opt-in and never enforced.** `foreman init` asks (default no); `foreman landing setup` changes it; `ws new --no-landing` opts one workstream out; projects without it keep D5's closeout unchanged. Forge problems are warnings in `doctor` and never block `ws close`.
- **Set up at onboarding:** forge, project, fork layout, base branch and gate are detected from the remotes; mode, branch template, draft and a token command per forge host are asked; the token is checked at once.

**Answers to the open questions** (user, 2026-11-04 → 11-16):
1. *Merge target:* per repo, chosen at onboarding: `pr` for work (GitLab MRs, self-hosted) and open-source (GitHub PRs, often from forks) repos, `ff` for personal ones. No silent default.
2. *Who asks:* the inbox (with a toast), plus a one-line notice to the orchestrator. The worker's session has ended, so its pane doesn't ask. herdr can't open a popup programmatically.
3. *Landing without a press:* no. Opening a PR is visible to colleagues and maintainers. A per-type `auto_open = "draft"` could come later.
4. *Keep working:* a run on the same workstream and branch with the user's note (e.g. relayed review comments). New scope becomes a follow-up.
5. *Park:* open, idle, branch and PR kept, no nudges. *Close:* abandon and archive (the PR is left alone; foreman says it's still open).
6. *Follow-up format:* `followups.jsonl`, `{title, goal, type, mode}`, parsed as untrusted input. Filed at Land, not at merge.
7. *Queues:* `proposals.jsonl` is the follow-up queue; `backlog.md` stays for free-text notes.
8. *Token storage:* a command per forge host, in the project config or, if the user chooses at onboarding, the global config.

**Alternatives considered:** A, the worker stays alive and asks in its pane (one decision, but the session lives until you answer); C, a batch landing queue (the inbox already behaves like one); D, the orchestrator drives landing in conversation (it would do workstream work, against the 10-30 incident's rule); E, land automatically when gates pass (unreviewed code on main; maybe later per type, docs-only); F, PR-only (needs a forge; personal projects don't); G, just auto-send the closeout prompt and merge on approve (cheapest, but keeps sessions alive and follow-ups loose).

**Not built yet:** merging through the forge API, GitLab MRs from forks (config fields reserved), reading PR comments automatically, CI status in the ready item, enforcing landing order for stacked branches (Land fails on conflicts instead), updating a PR's title/body when the packet changes, autonomous and remote workers (the remote-agents design; the packet and proposals queue are shaped for them).

## Findings from the second build (2026-10-29)

Tested in an isolated herdr (`XDG_CONFIG_HOME`): two toy projects side by side, pi on the local Qwen for orchestrators and workers, gemma4 for summaries.

- **Two projects in one herdr work** with separate orchestrators, watchers, inboxes and agent names.
- **Closeout works end to end.** A research workstream's closeout proposed a docs page and a follow-up. On approval the worker created `foreman-docs/` with a README index and the page, added the backlog item and marked it applied. foreman then closed its tab and archived it. The drafts were good on a local model.
- **Worktree removal tripped on `__pycache__/`** left by the worker's test run. foreman now treats a short list of regenerable caches as disposable; any modified tracked file or other untracked file still blocks removal and is listed.
- **Restored agents appear in herdr's list before the harness has loaded**, so the first prompt after a restart failed and its error was swallowed. Prompts are now held until the agent is idle, then checked: one typed while pi was loading landed in the input box without the Enter, so foreman presses Enter once if nothing happens within five seconds.
- **Native resume restores the conversation, not the interrupted turn.** A worker that was mid-task sat idle after the restart. foreman now nudges workers whose last recorded state was working or blocked.
- **Reopening plain `herdr` restarted both watchers within a second** via the tab-bar supervisor, and `foreman down` / `up` round-tripped a project, with workers restarting from brief + report. A restarted worker whose report already said done confirmed it and stopped, as the resume kickoff now asks.
- **The user had switched toasts to `delivery = "terminal"`** (desktop notifications) in the first prototype's block. `setup-herdr` now preserves that.
- **Worker panes run the user's shell profile**, so `foreman` inside them resolves through the user's PATH (`~/.local/bin` first here), not the binary that started herdr. Install before relying on new commands inside agents.
- Not exercised live: the stale and too-many-open nudges (need days or many turns), relay mode with several projects, and the docs-folder seeding beyond the orchestrator's offer.

## Findings: popups, attach and plugins (2026-10-29)

From a background investigation in an isolated herdr (headless server, a fake PTY client, pi as the agent). Verified unless marked otherwise.

**Live panes in popups work.**
- A `[[keys.command]] type = "popup"` whose command is `herdr agent attach <name|pane> --takeover` (for agents) or `herdr terminal attach <terminal_id> --takeover` (for plain shells) shows the live pane. You can type into it, and herdr keeps tracking the agent's state.
- Closing the popup leaves the pane's process running in its tab. The pane resizes to the popup while attached and back afterwards.
- Detaching is `ctrl+b q`, or the attach process exiting. Pressing the hotkey again does **not** close the popup: while attached, every key goes into the pane.
- One writable owner per terminal. `--takeover` evicts the current owner silently. Merely viewing a pane in a tab doesn't count as owning it.
- A popup command can be a script that resolves its target at open time (about 3 ms for two CLI calls).
- Popup commands get `HERDR_ACTIVE_WORKSPACE_ID`, `HERDR_ACTIVE_TAB_ID`, `HERDR_ACTIVE_PANE_ID`, `HERDR_ACTIVE_PANE_CWD`, `HERDR_SOCKET_PATH` and `HERDR_ENV=1`, but no `HERDR_SESSION`. They start in the active pane's cwd, with the server's PATH.
- Terminal ids change on restart; pane ids and agent names survived. Attach by agent name or pane id, and look terminal ids up fresh every time.
- Popups are session-modal and one at a time; herdr keys don't work while one is open.
- `herdr terminal session observe` is a read-only stream of frames that many readers can share. `terminal session control` is read-write and needs ownership.

**herdr has a plugin system.**
- A plugin is a directory with a `herdr-plugin.toml` (`id`, `name`, `version`, `min_herdr_version`), registered with `herdr plugin link <dir>` or `herdr plugin install owner/repo`.
- Manifest sections:
  - `[[events]]` hooks (verified: `pane.agent_status_changed` and `tab.created`, with the payload in `HERDR_PLUGIN_EVENT_JSON`)
  - `[[actions]]`, bound with `type = "plugin_action"` keys; each gets `HERDR_PLUGIN_CONTEXT_JSON` with the focused workspace, tab and pane and the cwd
  - `[[panes]]` with `placement = "popup|overlay|split|tab|zoomed"`
  - `[[startup]]` commands, run after each server start (not supervised)
  - `[[link_handlers]]`
- `herdr plugin pane open` opens a popup **from code**; `popup.close` closes it.
- Plugin state goes under `XDG_STATE_HOME`. Isolated tests must set it too: the investigation briefly left plugin state and pi session files in the real home, and deleted them.

**What this means for foreman:**
- The scratch session and the persistent terminal can be floating windows without a fork.
- The inbox can open a blocked worker's live terminal to answer a dialog, instead of sending keys blindly.
- The orchestrator can be a popup.
- Plugin event hooks plus `report-metadata` tokens could replace the polling watcher and the tab-bar supervisor (the metadata half is not tested).

## Plugin spike (2026-10-29)

foreman now ships a herdr plugin, written and linked by `foreman setup-herdr` (under `$XDG_DATA_HOME/foreman/herdr-plugin/`). Verified in an isolated herdr:

- **Hooks fire reliably and fast.** `[[startup]]`, plus `[[events]]` on `workspace.created`, `workspace.closed` and `pane.agent_status_changed`. A pi turn produced working → idle events within about 0.4s of the prompt. Each event runs `foreman hook event` as a short process.
- **Hook environment.** Hooks get `HERDR_PLUGIN_EVENT_JSON` (the event's own `pane_id` / `workspace_id`), `HERDR_PLUGIN_CONTEXT_JSON` (the *focused* workspace, tab and pane, not necessarily the event's) and `HERDR_SOCKET_PATH`, but no `HERDR_SESSION`.
- **The socket's `events.subscribe` needs a `pane_id` for pane events,** so a watcher would have to subscribe per pane and resubscribe as workers come and go. Not worth it.
- **Design adopted:**
  - The plugin hook wakes the project's watcher through a datagram socket (`$XDG_DATA_HOME/foreman/run/<project>.sock`, outside the project because socket paths are limited to about 100 bytes). If no watcher is listening, the hook starts one, so it doubles as the supervisor.
  - The startup hook starts watchers for every open project when herdr starts.
  - The watcher keeps a fallback poll: 1.5s until the first wake, 6s after.
  - The tab-bar supervisor is gone. `setup-herdr` removes the line older versions added; `foreman status-bar` remains as an optional inbox-count entry.
- **Key conflicts found.** herdr binds `prefix+` `? b c e g h j k l n o p q r s v w x z`, tab and minus, plus several shift and alt keys. foreman's old `prefix+n` was overriding herdr's next-tab key. Keys now live in `[keys]`: inbox `prefix+i`, next `prefix+shift+i`. Planned: `m` / `shift+m` orchestrator (switch / float), `a` scratch ("ask"), `t` terminal, `d` dock, `f` new workstream. `doctor` flags clashes.
- **Fixed along the way:**
  - Bootstrap "restarted" a workstream that `ws new` was creating at that moment; it now skips workstreams created after bootstrap began.
  - `foreman down` produced false "exited" events for its workers, and the watcher took up to 12s to exit. Exits are now reported only while the space is still open, and the watcher exits as soon as its workspace is gone.
- **Isolation gap.** pi writes its session files to `~/.pi/agent/sessions/` whatever `XDG_*` says, so toy-project runs leave folders named `--tmp-claude-1000-*` there.

## Phase 2: views, scratch, terminal, docking (2026-10-29)

Built and tested live in an isolated herdr, driving a real client by keystrokes:

- **Scratch (`prefix+a`)** created its tab and started pi with a context kickoff on first press, and opened a floating attach. A question typed in the popup was answered ("api/app.py (line 1)"). After `ctrl+b q` the popup closed and the agent kept running.
- **Terminal (`prefix+t`)** keeps shell state across closing and reopening (an exported variable survived).
- **Orchestrator switch (`prefix+m`)** goes to the orchestrator and, pressed again, back to the previous pane. Plain-shell panes are returned to by tab, since herdr can't focus an arbitrary pane by id.
- **Docking (`prefix+d` picker, `foreman dock`)**:
  - The first dock splits right of the orchestrator; further docks stack below it. Records live in `.foreman/logs/docks.json`.
  - Floating a docked session doesn't take it over: it jumps to the orchestrator, with a toast.
  - After a herdr restart, bootstrap re-runs the view command in the restored dock panes, which re-attach with fresh terminal ids.
  - Bug found: `pane run` prints nothing on success, so the JSON-expecting wrapper failed and the dock went unrecorded. Fixed.
- **Requests (`prefix+f`, `foreman request`)**:
  - Sent straight to an idle orchestrator. A request sent while it was busy waited in `.foreman/outbox/` and was delivered by the watcher when it went idle.
  - The local orchestrator turned the two requests into a `research` and a `feature` workstream.
  - `ctrl+d` in the box created `rename-health-to-status` directly.
  - Every `ws new` now toasts "▶ started".
- **Inbox live answer.** Enter on a blocked Claude worker (its trust dialog) attached to its real terminal inside the inbox popup; `ctrl+b q` returned to the inbox.

## Incident: the orchestrator received a worker's closeout (2026-10-30)

On the real AI-Projects run, `foreman ws finish review-parked` delivered the closeout request to the orchestrator, which then drafted review-parked's closeout itself. What happened, from the watcher log and herdr's state:

1. review-parked's Claude exited, leaving a plain shell in its tab. Before the next herdr restart, the `orchestrator` tab was gone (most likely closed when the old orchestrator was exited).
2. On bootstrap, `OrchestratorPane()` found no `orchestrator` tab and **fell back to the first tab**, review-parked's. It started the new orchestrator in review-parked's pane.
3. From then on, foreman considered review-parked alive (its recorded pane held *an* agent), and everything addressed to review-parked went to the orchestrator.
4. The orchestrator took the message as its own, did the worker's job, then guessed at causes and reported the guesses as findings: a separate workers' session (there wasn't one), pane IDs reassigned at the restart (they weren't), all twelve requests untrustworthy (eleven were routed correctly). Separately, its Claude session predated `foreman install`'s new Claude settings (Claude reads them at startup), so it still hit the sandbox.

**Fixes:**
- `OrchestratorPane` only accepts the tab labelled `orchestrator`. Bootstrap recreates the tab if it's missing.
- **Ownership check before every use of a workstream's pane** (`space.WorkerPane`): prompt, keys, peek, focus, views, inbox actions, closeout, approve, close, restart, and the watcher's liveness and event attribution. A pane counts as the workstream's only if its agent carries the workstream's agent name, or, with no named agent, it sits in the workstream's own tab. Otherwise foreman refuses with "its pane now belongs to something else (w1:p2 holds X; run `foreman ws resume <name>`)". `ws close` still closes the workstream but leaves someone else's pane alone. `ws resume` gives the worker a new pane.
- **Every foreman message names its recipient**: `[foreman → review-parked] …`, `[foreman → orchestrator] …`. The brief and the orchestrator guide say a message addressed to someone else is a misroute: say so, don't act.
- The orchestrator guide adds: never do a workstream's work, including its closeout, and when foreman misbehaves, report observations and label guesses as guesses.
- Reproduced in an isolated herdr (orchestrator tab closed, worker exited, restart): the orchestrator got a new tab, the worker restarted in its own pane, and all pane-using commands refused a pane renamed to another agent.

**Also learned:** in this Linux sandbox, Claude Code's `sandbox.network.allowUnixSockets` entry for herdr's socket didn't allow the connection. `excludedCommands: ["foreman"]` is what makes the orchestrator work, because foreman then runs outside the sandbox. Running Claude sessions need a restart after `foreman install`.

## Open questions

1. **Which candidate feels right?** That's the point of the prototype. Try each on a real day: `foreman mode notifier|inbox|relay`.
2. **Should the orchestrator resume or rehydrate?** Native resume brings back its full transcript, and bootstrap also nudges it to re-read the digest. Starting it fresh from the ledger every time would avoid bloat but loses conversational continuity.
3. ~~**Merge flow.**~~ Answered by D11 (landing): foreman merges (ff) or opens the PR/MR and watches it. Without landing, merging is still yours.
4. **Toast noise.** herdr's built-in state toasts and foreman's summarized toasts both fire in `delivery = "herdr"` mode. We need to see whether that's too much.
5. **Deploy guardrails.** The `deploy` type relies on the brief telling the worker to check in. Hard enforcement (hooks, agent-lockdown) is out of scope for now.
6. **Personal-assistant overlap.** Pinned messages, forking from a message, and Jira integration beyond instructions probably belong in the PA (`projects/personal-assistant`). foreman should stay a project tool.
7. **Relationship to mycelium / agent-orchestrator.** Both overlap (worker capture, the roster). foreman deliberately doesn't depend on mycelium yet; revisit if per-turn capture becomes necessary.
8. **Closeout approval granularity.** Approval is all or nothing (with an optional note). If drafts often need one edit dropped, per-section approval in the inbox may be worth it.
9. **Committing folded-in docs.** Workers apply doc edits but don't commit them, so you review the diff. In `commit` mode that leaves uncommitted docs in the main checkout after each closeout; an explicit "commit docs" step (or a docs branch) may be cleaner.
10. **Closeouts racing each other.** Two closeouts drafted at the same time both see the docs folder as it was before either applied; the second can propose an edit the first already made. Probably fine (the worker re-reads before applying), but worth watching.
11. **Tabs duplicating the agent list (deferred, 2026-10-29).** With live-pane popups, a fork may not be needed: workers could stay in tabs while you reach them through popups. The user preferred the first prototype's feel: one main view that agents take turns in, with no tab bar. Options: (A) a space per workstream again, labelled `project · name`, in the shared session (no fork; the spaces list grows with every workstream); (B) a tab per workstream with the tab bar always hidden (needs a herdr option that doesn't exist: patch or upstream request); (C) the existing `panes` layout, if hidden narrow panes turn out to render fine (sizes were measured, rendering wasn't). Revisit if foreman forks herdr.
12. **What the inbox is for.** Not yet clear from real use: the AI-Projects run left 16 of 20 items unacknowledged, but foreman hadn't been driven hard yet. Keep `inbox` as the default mode until there's more use to judge by.
13. **Direct workstreams get no worktree.** `ctrl+d` in the request box uses the default type with no repos, so a `feature` started that way works in the main checkouts. Options: the box offers a repo picker, direct mode defaults to a read-only type, or code-changing types refuse to start without repos.
