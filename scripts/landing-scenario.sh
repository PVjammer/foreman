#!/usr/bin/env bash
# Landing scenario: throwaway projects that exercise landing end to end with the real foreman
# CLI, against local bare repos and a fake GitHub/GitLab API. No herdr, no agents, no network:
#
#   - HOME/XDG dirs and git's global config point into a temp dir, so nothing of yours is read
#     or written (~/.config/foreman, ~/.gitconfig, herdr's config all untouched).
#   - HERDR_BIN_PATH is a stub that logs calls and fails, so foreman never reaches a real herdr.
#   - Forge remotes (git@github.com:…, git@gitlab.scenario.test:…) are mapped to local bare
#     repos with url.insteadOf, so pushes land on disk. The fake forge (cmd/fakeforge) answers
#     the API. Nothing leaves the machine.
#   - Workers are simulated: the script commits on the worktree branch and writes the landing
#     packet the brief asks for, then runs `foreman ws ready` (the watcher does that on "done").
#
#   scripts/landing-scenario.sh [dir]    # default: a new temp dir; output is the transcript
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:-$(mktemp -d "${TMPDIR:-/tmp}/fm-landing.XXXXXX")}
mkdir -p "$dir"/{bin,config,data,home,remotes}
dir=$(cd "$dir" && pwd)

export HOME=$dir/home XDG_CONFIG_HOME=$dir/config XDG_DATA_HOME=$dir/data
export GIT_CONFIG_GLOBAL=$dir/gitconfig GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=scenario GIT_AUTHOR_EMAIL=s@example.com GIT_COMMITTER_NAME=scenario GIT_COMMITTER_EMAIL=s@example.com
git config --global init.defaultBranch main
git config --global advice.detachedHead false

(cd "$repo" && go build -o "$dir/bin/foreman" ./cmd/foreman && go build -o "$dir/bin/fakeforge" ./cmd/fakeforge)
cat > "$dir/bin/herdr" <<EOF
#!/bin/sh
echo "\$*" >> "$dir/herdr-calls.log"
echo '{"error":"scenario: no herdr"}' >&2
exit 1
EOF
chmod +x "$dir/bin/herdr"
export HERDR_BIN_PATH=$dir/bin/herdr PATH=$dir/bin:$PATH

"$dir/bin/fakeforge" -token fake-token -user nick -addr-file "$dir/forge-addr" >"$dir/fakeforge.log" 2>&1 &
forge_pid=$!
trap 'kill $forge_pid 2>/dev/null || true' EXIT
for _ in $(seq 50); do [ -s "$dir/forge-addr" ] && break; sleep 0.1; done
FORGE=$(cat "$dir/forge-addr")

step() { printf '\n=== %s\n' "$*"; }
run() {
  printf '$ %s\n' "$*"
  local rc=0
  "$@" > "$dir/.out" 2>&1 || rc=$?
  sed 's/^/  /' "$dir/.out"
  if [ $rc -ne 0 ]; then echo "  (exit $rc)"; fi
}
parked() { printf '  message parked for the worker:\n'; sed 's/^/    /' ".foreman/ws/$1/kickoff.txt"; echo; }
fake() { curl -fsS -X "$1" "$FORGE$2"; echo; }

# bare <host-path>: a bare repo standing in for a forge repo, seeded from a template commit.
bare() {
  local b=$dir/remotes/$1.git
  git init -q --bare "$b"
  echo "$b"
}

# project <name> <remote-url...>: a one-repo project with an initial commit pushed to its remotes.
project() {
  local name=$1; shift
  local p=$dir/$name
  mkdir -p "$p" && cd "$p"
  git init -q
  printf 'hello\n' > app.txt
  printf '#!/bin/sh\ngrep -q hello app.txt\n' > test.sh && chmod +x test.sh
  printf '# %s\n' "$name" > AGENTS.md
  git add -A && git commit -qm initial
}

# work <ws> <file> <line>: the "worker" commits a change and writes its landing packet.
work() {
  local ws=$1 file=$2 line=$3 wt
  wt=$(cd "$(pwd)/.foreman/ws/$ws/root" && pwd)
  printf '%s\n' "$line" >> "$wt/$file"
  git -C "$wt" add -A && git -C "$wt" commit -qm "$ws: $line"
  local out=.foreman/ws/$ws
  cat > "$out/landing.md" <<EOF
---
status: ready
---
## Title
$ws: add "$line"

## Description
Adds "$line" to $file.

## Tests
./test.sh: pass

## Summary
Added "$line".
EOF
  printf -- '---\nstatus: done\n---\n## Summary\nDone.\n' > "$out/report.md"
}

followups() {
  cat > ".foreman/ws/$1/followups.jsonl" <<'EOF'
{"title":"Add a goodbye line","goal":"app.txt should also say goodbye","type":"feature","mode":"interactive"}
{"title":"Document app.txt","goal":"A short page on what app.txt is for","type":"research","mode":"autonomous"}
{"title":"\u001b[31mRewrite everything\u001b[0m $(touch PWNED)","goal":"no","type":"feature; rm -rf /"}
EOF
}

# ---------------------------------------------------------------------------------------------
step "A. Opt-out: a project that doesn't use landing keeps today's closeout"
project plain
run foreman init -y --name plain --docs-mode local
run foreman landing status
run foreman ws new w1 --detached --type feature --repos . --goal "anything"
printf '  brief has a Landing section: '; grep -q '^## Landing' .foreman/ws/w1/brief.md && echo yes || echo no

# ---------------------------------------------------------------------------------------------
step "B. Personal project: local fast-forward, follow-ups, docs patch"
project personal
run foreman init -y --name personal --docs-mode local --landing on --landing-mode ff --landing-gate ./test.sh
run foreman landing status
run foreman ws new feat --detached --type feature --repos . --goal "say hi too"
printf '  brief has a Landing section: '; grep -q '^## Landing' .foreman/ws/feat/brief.md && echo yes || echo no
work feat app.txt hi
followups feat
cat > .foreman/ws/feat/docs.patch <<'EOF'
--- /dev/null
+++ b/app.md
@@ -0,0 +1 @@
+app.txt greets people. Lines are added by workstreams.
EOF
run foreman ws ready feat
run foreman inbox --list
run foreman ws show feat
run foreman ws land feat --start 1 --drop 3 --mode 1=interactive
run git log --oneline main
run foreman proposals
run cat foreman-docs/app.md
run foreman ws list --all
printf '  PWNED created by follow-up text: '; [ -e PWNED ] && echo YES || echo no

step "B2. Personal project: main moved on, so it goes back to the worker"
run foreman ws new feat2 --detached --type feature --repos . --goal "say hey"
work feat2 app.txt hey
printf 'notes\n' > NOTES.md && git add NOTES.md && git commit -qm "main moved on"
run foreman ws ready feat2
run foreman ws land feat2
run foreman ws list
parked feat2
wt=.foreman/ws/feat2/root
git -C "$wt" rebase -q main
run foreman ws ready feat2
run foreman ws land feat2
run git log --oneline main

# ---------------------------------------------------------------------------------------------
step "C. GitHub project: Land pushes and opens a PR; keep working updates it; merged → landed"
b=$(bare github/nick/oss)
git config --global url."$dir/remotes/github/".insteadOf "git@github.com:"
project oss
git remote add origin git@github.com:nick/oss.git
git push -q origin main
run foreman init -y --name oss --docs-mode local --landing on --landing-mode pr \
  --forge-token-cmd "echo fake-token" --forge-api "$FORGE" --forge-save global
run foreman landing status
printf '  global config now has:\n'; sed 's/^/    /' "$XDG_CONFIG_HOME/foreman/config.toml" | grep -A4 forges
run foreman ws new fix --detached --type feature --repos . --goal "say howdy"
work fix app.txt howdy
followups fix
run foreman ws ready fix
run foreman ws land fix --drop 2,3
run git --git-dir "$b" log --oneline fm/fix
run fake GET /_fake/requests
run foreman landing poll
step "C2. Review asks for a change: keep working, same PR"
run foreman ws keep fix "reviewer: also say yo"
parked fix
work fix app.txt yo
run foreman ws ready fix
run foreman ws land fix
run fake GET /_fake/requests
step "C3. A maintainer squash-merges on the forge (main never contains fm/fix)"
run fake POST "/_fake/set?id=1&state=merged"
run foreman landing poll
run foreman ws list --all
run foreman proposals

# ---------------------------------------------------------------------------------------------
step "D. GitHub fork: push to your fork, PR on upstream"
bare github/upstream-org/lib >/dev/null
bare github/nick/lib >/dev/null
git config --global url."$dir/remotes/github/".insteadOf "git@github.com:"
git config --global --add url."$dir/remotes/github/".insteadOf "https://github.com/"
project lib
git remote add upstream https://github.com/upstream-org/lib.git
git remote add origin git@github.com:nick/lib.git
git push -q upstream main && git push -q origin main
run foreman init -y --name lib --docs-mode local --landing on --landing-mode pr \
  --forge-token-cmd "echo fake-token" --forge-api "$FORGE"
run foreman landing status
run foreman ws new fork-fix --detached --type feature --repos . --goal "say g'day"
work fork-fix app.txt "g'day"
run foreman ws ready fork-fix
run foreman ws land fork-fix
run git --git-dir "$dir/remotes/github/nick/lib.git" branch --list 'fm/*'
run git --git-dir "$dir/remotes/github/upstream-org/lib.git" branch --list 'fm/*'
run fake GET /_fake/requests

# ---------------------------------------------------------------------------------------------
step "E. Self-hosted GitLab: MR closed without merging → keep working → new MR → merged"
gb=$(bare gitlab/team/svc)
git config --global url."$dir/remotes/gitlab/".insteadOf "git@gitlab.scenario.test:"
project svc
git remote add origin git@gitlab.scenario.test:team/svc.git
git push -q origin main
export SVC_TOKEN=fake-token
run foreman init -y --name svc --docs-mode local --landing on --landing-mode pr \
  --forge-token-env SVC_TOKEN --forge-api "$FORGE" --landing-branch "{ticket}-{name}"
run foreman landing status
run foreman doctor
run foreman ws new svc-fix --detached --type feature --repos . --ticket OPS-7 --goal "say hello there"
work svc-fix app.txt "hello there"
run foreman ws ready svc-fix
run foreman ws land svc-fix
run git --git-dir "$gb" branch --list
mr=$(curl -fsS "$FORGE/_fake/requests" | python3 -c 'import json,sys; print([r["id"] for r in json.load(sys.stdin) if r["forge"]=="gitlab"][-1])')
run fake POST "/_fake/set?id=$mr&state=closed"
run foreman landing poll
run foreman inbox --list
run foreman ws keep svc-fix "reviewer closed it: wording should be 'hello, there'"
parked svc-fix
work svc-fix app.txt "hello, there"
run foreman ws ready svc-fix
run foreman ws land svc-fix
mr=$(curl -fsS "$FORGE/_fake/requests" | python3 -c 'import json,sys; print([r["id"] for r in json.load(sys.stdin) if r["forge"]=="gitlab"][-1])')
run fake POST "/_fake/set?id=$mr&state=merged"
run foreman landing poll
run foreman ws list --all

# ---------------------------------------------------------------------------------------------
step "F. A landing project, but this workstream opts out (--no-landing)"
cd "$dir/personal"
run foreman ws new manual --detached --no-landing --type feature --repos . --goal "do it the old way"
printf '  brief has a Landing section: '; grep -q '^## Landing' .foreman/ws/manual/brief.md && echo yes || echo no
work manual app.txt manual
run foreman ws ready manual

step "herdr calls foreman attempted (stub; nothing reached a real herdr)"
sort "$dir/herdr-calls.log" | sed -E 's/(notify [^ ]+ [^ ]+).*/\1 …/' | cut -c1-110 | uniq -c | sed 's/^/  /'
echo
echo "scenario dir: $dir"
