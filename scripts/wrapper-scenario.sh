#!/usr/bin/env bash
# Scenario run for wrapped workers, in an isolated herdr (own XDG dirs, own session) with
# scripts/dummy-wrapper standing in for agentbox. Uses pi (local model) as the harness.
#
#   scripts/wrapper-scenario.sh [dir]      # dir must be short: herdr's socket path is ~100 bytes max
#
# Checks, in order:
#   1. a wrapped worker starts under the wrapper, is detected as pi, gets its kickoff and
#      writes its report; the wrapper saw the FOREMAN_* env and none of HERDR_ENV/HERDR_PANE_ID
#   2. doctor passes the wrapped profile's check and fails a wrapper that isn't on PATH
#   3. fail closed: missing wrapper, wrapper exiting non-zero, harness hidden from detection
#   4. after a herdr restart the wrapped worker comes back through the wrapper, not bare
set -uo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:-$(mktemp -d "${TMPDIR:-/tmp}/fmw.XXXX")}
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
export XDG_CONFIG_HOME=$dir/cfg XDG_STATE_HOME=$dir/state XDG_DATA_HOME=$dir/data
unset HERDR_SESSION HERDR_SOCKET_PATH HERDR_ENV HERDR_PANE_ID HERDR_TAB_ID HERDR_WORKSPACE_ID
export PATH=$here/bin:$here/scripts:$PATH FOREMAN_WRAP_TIMEOUT=${FOREMAN_WRAP_TIMEOUT:-25s}
sess=fmwrap
H="herdr --session $sess"
fails=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }

(cd "$here" && go build -o bin/foreman ./cmd/foreman) || exit 1

mkdir -p "$XDG_CONFIG_HOME/foreman"
cat > "$XDG_CONFIG_HOME/foreman/config.toml" <<'EOF'
[herdr]
session = "fmwrap"
[orchestrator]
profile = "pi"
[scratch]
profile = "pi"
[summarizer]
enabled = false
[profiles.boxed]
harness = "pi"
wrapper = ["dummy-wrapper"]
wrapper_check = ["sh", "-c", "test \"$DUMMY_BOX\" = 1"]
use_for = "scenario: wrapped pi"
[profiles.boxed-missing]
harness = "pi"
wrapper = ["no-such-wrapper"]
[profiles.boxed-fail]
harness = "pi"
wrapper = ["dummy-wrapper", "--fail", "7"]
[profiles.boxed-hidden]
harness = "pi"
wrapper = ["dummy-wrapper", "--setsid"]
EOF

proj=$dir/proj
rm -rf "$proj" && mkdir -p "$proj" && cd "$proj"
git init -q && echo "# wrapper scenario" > AGENTS.md && echo "hello" > README.md
git add -A && git -c user.name=t -c user.email=t@t commit -qm init
foreman init --yes --name wrapscn --orchestrator pi --worker pi --scratch pi --docs-mode local </dev/null >/dev/null || exit 1

$H server stop >/dev/null 2>&1
(setsid $H server >"$dir/herdr.log" 2>&1 &)
for _ in $(seq 1 25); do $H workspace list >/dev/null 2>&1 && break; sleep 0.2; done

goal='Write the report now: status done, Summary "wrapped hello". Do nothing else.'

# 1. a wrapped worker works end to end
if foreman ws new boxed --type feature --repos . --profile boxed --goal "$goal"; then pass "ws new boxed"; else fail "ws new boxed"; fi
m=.foreman/ws/boxed
pane=$(sed -n 's/.*"pane_id": *"\([^"]*\)".*/\1/p' $m/meta.json)
if $H agent get "$pane" 2>/dev/null | grep -q '"agent":"pi".*"name":"wrapscn-boxed"\|"name":"wrapscn-boxed".*"agent":"pi"'; then
  pass "herdr detects pi under the wrapper, named wrapscn-boxed"
else fail "detection/naming: $($H agent get "$pane" 2>&1 | head -c 300)"; fi
if $H pane process-info --pane "$pane" | grep -q 'launch.sh'; then pass "pi runs under foreman's launch script"; else fail "process tree"; fi
for v in FOREMAN_WORKSTREAM=boxed FOREMAN_WS_DIR= FOREMAN_WORKTREES= FOREMAN_HERDR_SOCKET= FOREMAN_WRITABLE=; do
  grep -q "^$v" $m/wrapper-env.log && pass "wrapper env has ${v%=}" || fail "wrapper env lacks $v"
done
grep -qE '^HERDR_(ENV|PANE_ID)=' $m/wrapper-env.log && fail "wrapper env still has HERDR_ENV/HERDR_PANE_ID" || pass "herdr integration env stripped"
foreman watch >"$dir/watch1.log" 2>&1 &
wpid=$!
for _ in $(seq 1 90); do grep -q '^status: done' $m/report.md 2>/dev/null && break; sleep 2; done
grep -q '^status: done' $m/report.md 2>/dev/null && pass "kickoff delivered; worker wrote its report" || fail "no report from the wrapped worker"
kill $wpid 2>/dev/null; wait $wpid 2>/dev/null

# 2. doctor
doc=$(foreman doctor 2>&1)
echo "$doc" | grep -q '✔ profile boxed: sandbox active' && pass "doctor: boxed check passes" || fail "doctor boxed: $(echo "$doc" | grep 'profile boxed')"
echo "$doc" | grep -q '✖ profile boxed-missing: wrapper no-such-wrapper not on PATH' && pass "doctor: missing wrapper flagged" || fail "doctor missing"

# 3. fail closed
out=$(foreman ws new missing --type feature --profile boxed-missing --goal x 2>&1) && fail "missing wrapper started" || {
  [ ! -e .foreman/ws/missing ] && pass "missing wrapper refused before any setup: $out" || fail "missing wrapper left state"; }
out=$(foreman ws new failing --type research --profile boxed-fail --goal x 2>&1) && fail "failing wrapper started" || pass "wrapper exit 7 fails the workstream: $(echo "$out" | head -1)"
grep -q 'wrapped launch failed' .foreman/inbox.jsonl && pass "failure is in the inbox" || fail "no inbox item"
p=$(sed -n 's/.*"pane_id": *"\([^"]*\)".*/\1/p' .foreman/ws/failing/meta.json)
$H agent get "$p" >/dev/null 2>&1 && fail "something runs in failing's pane" || pass "nothing runs in failing's pane"
out=$(foreman ws new hidden --type research --profile boxed-hidden --goal x 2>&1) && fail "hidden harness accepted" || pass "undetected harness times out: $(echo "$out" | head -1)"
p=$(sed -n 's/.*"pane_id": *"\([^"]*\)".*/\1/p' .foreman/ws/hidden/meta.json)
sleep 1
pids=$(pgrep -x pi | while read -r q; do [ "$(readlink /proc/$q/cwd)" = "$proj" ] && echo $q; done)
hidden_left=0
for q in $pids; do grep -q "FOREMAN_WORKSTREAM=hidden" /proc/$q/environ 2>/dev/null && hidden_left=1; done
[ $hidden_left = 0 ] && pass "hidden harness killed (no pi from it left, even in its own session)" || fail "hidden harness still running after the timeout"

# 4. herdr restart: comes back through the wrapper
rm -f $m/wrapper-env.log
$H server stop >/dev/null 2>&1; sleep 2
(setsid $H server >>"$dir/herdr.log" 2>&1 &)
for _ in $(seq 1 25); do $H workspace list >/dev/null 2>&1 && break; sleep 0.2; done
foreman watch --bootstrap >"$dir/watch2.log" 2>&1 &
wpid=$!
for _ in $(seq 1 60); do grep -q 'FOREMAN_WORKSTREAM=boxed' $m/wrapper-env.log 2>/dev/null && break; sleep 1; done
sleep 3
pane=$(sed -n 's/.*"pane_id": *"\([^"]*\)".*/\1/p' $m/meta.json)
if grep -q 'FOREMAN_WORKSTREAM=boxed' $m/wrapper-env.log 2>/dev/null && $H pane process-info --pane "$pane" | grep -q launch.sh; then
  pass "after restart boxed runs under the wrapper again"
else fail "after restart: $($H pane process-info --pane "$pane" 2>&1 | head -c 300)"; fi
bare=0
for pn in $($H pane list --workspace "$($H workspace list | grep -o '"workspace_id":"[^"]*"' | head -1 | cut -d'"' -f4)" | grep -o '"pane_id":"[^"]*"' | cut -d'"' -f4); do
  info=$($H pane process-info --pane "$pn")
  echo "$info" | grep -q '"name":"pi"' && ! echo "$info" | grep -q launch.sh && [ "$pn" != "$($H agent get wrapscn-orch 2>/dev/null | grep -o '"pane_id":"[^"]*"' | cut -d'"' -f4)" ] && bare=1
done
[ $bare = 0 ] && pass "no bare pi in any worker pane" || fail "a bare pi is running in a worker pane"
kill $wpid 2>/dev/null; wait $wpid 2>/dev/null

$H server stop >/dev/null 2>&1; sleep 2
left=$(for q in $(pgrep -x pi); do grep -qz "^FOREMAN_PROJECT=wrapscn$" /proc/$q/environ 2>/dev/null && echo $q; done)
[ -z "$left" ] && pass "no wrapped pi outlives the herdr server" || { fail "wrapped pi left running: $left"; kill $left; }
echo "---"; echo "$fails failure(s); logs in $dir"
exit $((fails > 0))
