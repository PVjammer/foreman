#!/usr/bin/env bash
# Builds a toy multi-repo project and (optionally) kicks off three workstreams designed to
# produce each kind of event: one finishes, one asks a question, one hits a permission prompt.
#
#   scripts/scenario.sh setup [dir]     # create the project and `foreman init` it
#   scripts/scenario.sh start [dir]     # inside a running `foreman up`: start the three workstreams
#
# Workers default to pi (local model, no API cost). The permission-prompt case needs a harness
# that asks for approval (claude), so it uses --profile claude. Run setup twice with different
# dirs (and NAME=...) to get two projects side by side in one herdr.
set -euo pipefail

cmd=${1:-setup}
dir=${2:-${TMPDIR:-/tmp}/fm-scenario}

setup() {
  rm -rf "$dir" && mkdir -p "$dir" && cd "$dir"
  cat > AGENTS.md <<'EOF'
# Scenario project

A toy project with two repos, standing in for a real multi-repo product:

- `api/`  — a tiny Python HTTP API (`app.py`), tests in `test_app.py` (run `python -m pytest -q`).
- `site/` — the informational website (static HTML in `index.html`).
EOF
  mkdir api site
  cat > api/app.py <<'EOF'
def greet(name):
    return f"Hello, {name}!"


def health():
    return {"status": "ok"}
EOF
  cat > api/test_app.py <<'EOF'
from app import greet, health


def test_greet():
    assert greet("Ada") == "Hello, Ada!"


def test_health():
    assert health()["status"] == "ok"
EOF
  cat > site/index.html <<'EOF'
<!doctype html>
<html><head><title>Acme</title></head>
<body><h1>Acme</h1><p>We make things.</p></body></html>
EOF
  for r in api site; do
    git -C "$r" init -q && git -C "$r" add -A && git -C "$r" -c user.name=scenario -c user.email=s@example.com commit -qm "initial"
  done
  foreman init --yes --name "${NAME:-$(basename "$dir")}" --orchestrator "${ORCH:-pi}" --worker "${WORKER:-pi}" --docs-mode "${DOCS_MODE:-commit}"
  echo "scenario project ready at $dir — next: cd $dir && foreman up"
}

start() {
  cd "$dir"
  foreman ws new greet-default --type feature --repos api \
    --goal "Make greet() return 'Hello, world!' when name is empty or None. Add a test. Run the tests and commit."
  foreman ws new site-tagline --type feature --repos site \
    --goal "Update the site's tagline to something better."
  foreman ws new api-check --type research --profile "${PERM_PROFILE:-claude}" \
    --goal "Run the api test suite (python -m pytest -q in api/) and report whether it passes. Don't change anything."
  foreman ws list
}

"$cmd"
