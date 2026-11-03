# Wrapped workers (harness wrappers)

*Added 2026-11-03.* A worker profile can run its harness inside a sandbox launcher such as
agentbox, so a harness with few guardrails (pi) can work on sensitive repos without ever
running unsandboxed. foreman still sees its status and reports.

```toml
[profiles.pi-boxed]
harness = "pi"
args = ["--model", "qwen3.8-27b"]
wrapper = ["agentbox", "--level", "1"]        # foreman runs: agentbox --level 1 pi --model qwen3.8-27b
wrapper_check = ["test", "-e", "/run/agentbox/session"]   # optional for agentbox (this is its default)
use_for = "work on agent-lockdown"
```

Wrappers are for **workers only**. An orchestrator or scratch profile with a wrapper is refused
(not started bare), and `foreman doctor` flags it.

## How it starts

herdr's `agent start --kind pi` always runs herdr's own canonical `pi`, so it has no hook for
a wrapper. For a wrapped profile foreman instead:

1. Resolves the wrapper on PATH. Missing → `ws new` fails before it makes worktrees or a pane.
2. Checks the pane is at its shell prompt, writes `.foreman/ws/<name>/launch.sh`, and types
   `sh '<that path>'` into the pane. The script exports the env contract below, unsets
   herdr's integration variables, and runs `<wrapper...> <harness> <args...>` as a child, so
   it can record the exit status in `.foreman/ws/<name>/wrapper-exit`.
3. Polls `herdr agent list` until herdr detects an agent of the profile's kind in that pane,
   renames it to the workstream's agent name, waits for it to be ready, and sends the kickoff
   (or parks it for the watcher, as for an unwrapped worker).

From then on prompts, status, events, reports and closeout work exactly as for any worker.

## Fail closed

The launch fails, and never falls back to the bare harness, when:

| Case | What happens |
|---|---|
| Wrapper not on PATH | `ws new` / `ws resume` errors out; nothing is created. |
| Wrapper exits (any status) before the harness is detected | Error with its exit status and the pane's last lines. |
| A different agent is detected | Error; everything under the pane's shell is killed. |
| Not detected within 90s (`FOREMAN_WRAP_TIMEOUT` overrides, for tests) | Error; everything under the pane's shell is killed (SIGTERM, then SIGKILL). Ctrl+C isn't enough: a harness in a new session ignores it and even survives the herdr server stopping. |

Each failure is recorded as a `failed` event, an inbox item (`blocked`, "wrapped launch
failed: …") and a toast. The pane is left open so you can read the wrapper's output.

**Restarts.** Native resume would run the bare harness, so wrapped workers never use it:

- foreman unsets `HERDR_ENV` and `HERDR_PANE_ID` for the wrapper. Without them a harness's
  herdr integration (pi's extension, Claude's hooks) can't report a session, so herdr has
  nothing to resume and restores a plain shell. herdr still tracks idle / working / blocked
  from the screen. **Verified:** with the integration env present, a herdr restart ran
  `pi --session …` bare in the pane of a worker that had been inside bwrap.
- As a backstop, if bootstrap finds any agent in a wrapped worker's pane after a restart, it
  closes the pane and restarts the worker through its wrapper. `ws resume` likewise refuses a
  busy pane and opens a new one.

## Env contract

foreman exports these to the wrapper. The wrapper decides how to expose the paths (bind
mounts, allow-lists); foreman only names them. Path lists are `:`-separated.

| Variable | Meaning |
|---|---|
| `FOREMAN_WRAPPED` | `1` |
| `FOREMAN_PROJECT`, `FOREMAN_PROJECT_ROOT` | Project name and root (read access is enough) |
| `FOREMAN_WORKSTREAM`, `FOREMAN_HARNESS` | Workstream name; the harness (herdr kind) being wrapped |
| `FOREMAN_CWD` | Where the harness starts (the workstream folder, or the root when it has no worktrees) |
| `FOREMAN_WORKTREES` | The workstream's git worktrees; must be writable |
| `FOREMAN_WS_DIR` | `.foreman/ws/<name>/`: brief, `report.md`, `closeout.md`; must be writable |
| `FOREMAN_WRITABLE` | Everything above that must be writable: worktrees + ws dir (+ cwd when there are no worktrees) |
| `FOREMAN_DOCS_DIR` | The project docs folder; written only when applying an approved closeout |
| `FOREMAN_HERDR_SOCKET` | herdr's socket, for running `foreman` / `herdr` inside the sandbox |

Also note: worktrees are git worktrees, so committing needs write access to the main repo's
`.git/worktrees/<name>/` and objects, and closeout's `foreman backlog add` writes
`.foreman/backlog.md`. A wrapper that exposes only `FOREMAN_WRITABLE` will block those steps.

## Detection constraint for wrappers

herdr detects an agent from the pane's **foreground process group**. A wrapper must keep the
harness in it:

- Fine: `exec`, a parent that waits (`sh -c`, Python `subprocess.call`), `systemd-run --scope`,
  `bwrap` without `--new-session`, PID namespaces.
- Not detected: `setsid`, `bwrap --new-session`, containers (pi-box: docker runs pi under
  containerd, outside the pane's process tree). Once foreman has typed the wrapper, `agent
  prompt` also refuses an agent that isn't the pane's foreground process, even if registered
  by hand with `pane report-agent`.

**agentbox today uses `bwrap --new-session`** (against TIOCSTI injection), so `agentbox pi`
is *not* detected (inferred from its source plus a `bwrap --new-session pi` test; agentbox
isn't installed on this machine yet) and a `wrapper = ["agentbox"]` worker fails closed with the timeout. It needs
an agentbox change (a seccomp filter blocking `TIOCSTI` instead of `--new-session`; this WSL
5.15 kernel has no `dev.tty.legacy_tiocsti` switch) before wrapped pi works under it.

## Doctor

For every profile with a wrapper, `foreman doctor` checks the wrapper is on PATH and runs a
test launch, `<wrapper...> <check...>` in the project root, which must exit 0. The check is
`wrapper_check`, or `test -e /run/agentbox/session` for agentbox. A wrapper with no known
check is reported as unverified. Each agentbox check is a real (logged) agentbox session.

## Testing

- Unit: `go test ./internal/project ./internal/workstream` (profile parsing, env contract,
  the launch script run with a dummy wrapper).
- Scenario: `scripts/wrapper-scenario.sh [short-dir]` runs an isolated herdr with
  `scripts/dummy-wrapper` (logs its env, execs the harness; `--fail N`, `--setsid`) and pi:
  start, detection, kickoff and report, doctor, the three fail-closed cases, and a herdr
  restart. 21 checks, all passing on 2026-11-03.
