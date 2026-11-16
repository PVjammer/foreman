package landing

import (
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PVjammer/foreman/internal/forge/fake"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rig is a throwaway project: root repo "." on main, a workstream "feat" with a worktree on
// fm/feat holding one commit, and hooks that record what landing asked for.
type rig struct {
	t         *testing.T
	p         *project.Project
	l         *ledger.Ledger
	m         *ledger.Meta
	env       *Env
	handbacks []string
	notes     []string
	orch      []string
	closed    bool
	ended     bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // never read the real ~/.config/foreman
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	run(t, root, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(root, "app.txt"), "one\n")
	write(t, filepath.Join(root, ".gitignore"), ".foreman/\n")
	run(t, root, "git", "add", "-A")
	run(t, root, "git", "commit", "-qm", "initial")
	cfg := project.Defaults()
	cfg.Name = "rig"
	cfg.Landing.Enabled = true
	cfg.Docs.Mode = "local"
	cfg.Landing.Repos = map[string]project.LandingRepo{".": {Mode: "ff", Base: "main"}}
	p := &project.Project{Root: root, Config: cfg}
	for _, d := range []string{"ws", "archive"} {
		_ = os.MkdirAll(p.Path(d), 0o755)
	}
	l := ledger.New(p)
	wsDir := p.WorkstreamDir("feat")
	wt := filepath.Join(wsDir, "root")
	run(t, root, "git", "worktree", "add", "-q", wt, "-b", "fm/feat")
	write(t, filepath.Join(wt, "app.txt"), "one\ntwo\n")
	run(t, wt, "git", "commit", "-qam", "feat: two")
	m := &ledger.Meta{Name: "feat", Kind: "feature", Goal: "add two", Landing: true, Cwd: wsDir,
		Worktrees: []ledger.Worktree{{Repo: ".", Path: wt, Branch: "fm/feat"}}}
	if err := l.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, p: p, l: l, m: m}
	r.env = &Env{P: p, L: l,
		Handback:     func(m *ledger.Meta, msg string) error { r.handbacks = append(r.handbacks, msg); return nil },
		EndSession:   func(*ledger.Meta) { r.ended = true },
		Close:        func(*ledger.Meta) error { r.closed = true; return nil },
		Notify:       func(_ *ledger.Meta, kind, s string) { r.notes = append(r.notes, kind+": "+s) },
		Orchestrator: func(s string) error { r.orch = append(r.orch, s); return nil },
	}
	return r
}

func (r *rig) wt() string { return r.m.Worktrees[0].Path }

func (r *rig) packet(followups string) {
	dir := OutDir(r.l, r.m.Name)
	write(r.t, filepath.Join(dir, "landing.md"), "---\nstatus: ready\n---\n## Title\nAdd two\n\n## Description\nAdds the second line.\n\n## Tests\n`true`: pass\n\n## Summary\nDone.\n")
	if followups != "" {
		write(r.t, filepath.Join(dir, "followups.jsonl"), followups)
	}
}

func (r *rig) ready() {
	r.t.Helper()
	if ok, why := r.env.CheckReady(r.m); !ok {
		r.t.Fatalf("not ready: %s", why)
	}
}

func (r *rig) reload() {
	m, err := r.l.Meta(r.m.Name)
	if err != nil {
		r.t.Fatal(err)
	}
	r.m = m
}

func TestCheckReady(t *testing.T) {
	r := newRig(t)
	if ok, why := r.env.CheckReady(r.m); ok || !strings.Contains(why, "no landing packet") {
		t.Fatalf("without packet: %v %q", ok, why)
	}
	r.packet("")
	write(t, filepath.Join(r.wt(), "stray.txt"), "x")
	if ok, why := r.env.CheckReady(r.m); ok || !strings.Contains(why, "stray.txt") {
		t.Fatalf("with uncommitted file: %v %q", ok, why)
	}
	os.Remove(filepath.Join(r.wt(), "stray.txt"))
	r.ready()
	if r.m.Phase != "landing" || r.m.Land.State != "ready" || !r.ended {
		t.Fatalf("after ready: phase %q state %+v ended %v", r.m.Phase, r.m.Land, r.ended)
	}
	if len(r.notes) != 1 || !strings.HasPrefix(r.notes[0], "land: ready to land: Add two (.: ff → main)") {
		t.Fatalf("notes = %v", r.notes)
	}
	// Projects without landing: never "ready", whatever the worker writes.
	r2 := newRig(t)
	r2.p.Config.Landing.Enabled = false
	r2.packet("")
	if ok, _ := r2.env.CheckReady(r2.m); ok {
		t.Fatal("landing applied with landing disabled")
	}
}

func TestLandFastForward(t *testing.T) {
	r := newRig(t)
	r.packet(`{"title":"Add three","goal":"third line","type":"feature","mode":"interactive"}
{"title":"Docs pass","goal":"","type":"research"}
{"title":"Not wanted","type":"spike"}
`)
	r.ready()
	out, err := r.env.Land(r.m, map[int]Choice{0: {Action: "start"}, 2: {Action: "drop"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, r.p.Root, "git", "rev-parse", "main"); got != run(t, r.p.Root, "git", "rev-parse", "fm/feat") {
		t.Fatal("main not fast-forwarded")
	}
	if !strings.Contains(out, "fast-forwarded main") || !strings.Contains(out, "1 to start, 1 queued, 1 dropped") {
		t.Fatalf("out = %q", out)
	}
	if r.m.Land.State != "landed" || !r.closed {
		t.Fatalf("state %+v closed %v", r.m.Land, r.closed)
	}
	props := r.l.Proposals()
	if len(props) != 3 || props[0].Status != "start" || props[1].Status != "queued" || props[2].Status != "dropped" || props[0].From != "feat" {
		t.Fatalf("proposals = %+v", props)
	}
	if len(r.orch) != 1 || !strings.Contains(r.orch[0], `"Add three"`) {
		t.Fatalf("orchestrator = %v", r.orch)
	}
}

func TestLandFFNotFastForwardGoesBack(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.p.Root, "other.txt"), "x")
	run(t, r.p.Root, "git", "add", "-A")
	run(t, r.p.Root, "git", "commit", "-qm", "main moved")
	r.packet("")
	r.ready()
	before := run(t, r.p.Root, "git", "rev-parse", "main")
	_, err := r.env.Land(r.m, nil)
	if !errors.Is(err, ErrHandedBack) || len(r.handbacks) != 1 || !strings.Contains(r.handbacks[0], "Rebase fm/feat onto main") {
		t.Fatalf("err %v handbacks %v", err, r.handbacks)
	}
	if run(t, r.p.Root, "git", "rev-parse", "main") != before || r.m.Land.State != "working" || r.m.Phase != "" {
		t.Fatalf("state after handback: %+v phase %q", r.m.Land, r.m.Phase)
	}
	// The worker rebases and reports done again: ready, and Land works.
	run(t, r.wt(), "git", "rebase", "-q", "main")
	r.ready()
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	if !r.closed {
		t.Fatal("not closed")
	}
}

func TestLandFFDirtyMainRefusedUntouched(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.p.Root, "app.txt"), "local edit\n")
	r.packet("")
	r.ready()
	_, err := r.env.Land(r.m, nil)
	if err == nil || errors.Is(err, ErrHandedBack) || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
	if r.m.Land.State != "ready" || len(r.handbacks) != 0 {
		t.Fatalf("state %+v handbacks %v", r.m.Land, r.handbacks)
	}
}

func TestLandFFGateFailsGoesBack(t *testing.T) {
	r := newRig(t)
	pol := r.p.Config.Landing.Repos["."]
	pol.Gate = "grep -q three app.txt || { echo 'want three'; exit 1; }"
	r.p.Config.Landing.Repos["."] = pol
	r.packet("")
	r.ready()
	_, err := r.env.Land(r.m, nil)
	if !errors.Is(err, ErrHandedBack) || !strings.Contains(r.handbacks[0], "want three") {
		t.Fatalf("err %v handbacks %v", err, r.handbacks)
	}
}

// prRig adds a bare "origin" and the fake forge, and switches "." to PR mode.
func prRig(t *testing.T, fork bool) (*rig, *fake.Server, string) {
	r := newRig(t)
	srv := fake.New("tok", "nick")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	bare := filepath.Join(t.TempDir(), "origin.git")
	run(t, r.p.Root, "git", "init", "-q", "--bare", bare)
	run(t, r.p.Root, "git", "remote", "add", "origin", bare)
	run(t, r.p.Root, "git", "push", "-q", "origin", "main")
	pol := project.LandingRepo{Mode: "pr", Base: "main", Remote: "origin", Forge: "github.com", Project: "PVjammer/rig", Branch: "fm/{name}"}
	if fork {
		up := filepath.Join(t.TempDir(), "upstream.git")
		run(t, r.p.Root, "git", "init", "-q", "--bare", up)
		run(t, r.p.Root, "git", "remote", "add", "upstream", up)
		run(t, r.p.Root, "git", "push", "-q", "upstream", "main")
		pol.Project, pol.HeadOwner = "upstream-org/rig", "nick"
	}
	r.p.Config.Landing.Repos["."] = pol
	r.p.Config.Forges = map[string]project.Forge{"github.com": {Kind: "github", API: ts.URL, TokenCmd: []string{"echo", "tok"}}}
	return r, srv, bare
}

func TestLandPRThenMerged(t *testing.T) {
	r, srv, bare := prRig(t, false)
	r.packet(`{"title":"later","type":"feature"}`)
	write(t, filepath.Join(OutDir(r.l, "feat"), "docs.patch"), "--- /dev/null\n+++ b/decisions.md\n@@ -0,0 +1 @@\n+Two lines now.\n")
	r.ready()
	out, err := r.env.Land(r.m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "opened https://") || r.m.Land.State != "in-review" || r.closed {
		t.Fatalf("out %q state %+v closed %v", out, r.m.Land, r.closed)
	}
	if got := run(t, r.p.Root, "git", "--git-dir", bare, "rev-parse", "fm/feat"); got != run(t, r.p.Root, "git", "rev-parse", "fm/feat") {
		t.Fatal("branch not pushed")
	}
	rq := srv.Requests()
	if len(rq) != 1 || rq[0].Title != "Add two" || rq[0].Base != "main" || !strings.Contains(rq[0].Body, "Adds the second line.") {
		t.Fatalf("requests = %+v", rq)
	}
	if len(r.l.Proposals()) != 1 {
		t.Fatal("follow-up not filed at Land")
	}
	// Nothing changes while the PR is open.
	if err := r.env.Poll(r.m); err != nil || r.m.Land.State != "in-review" {
		t.Fatalf("poll open: %v %+v", err, r.m.Land)
	}
	// Squash-merged on the forge: main in the bare repo doesn't contain fm/feat, and that's fine.
	srv.Set(1, "merged")
	if err := r.env.Poll(r.m); err != nil {
		t.Fatal(err)
	}
	if r.m.Land.State != "landed" || !r.closed {
		t.Fatalf("after merge: %+v closed %v", r.m.Land, r.closed)
	}
	if b, err := os.ReadFile(filepath.Join(r.p.DocsDir(), "decisions.md")); err != nil || string(b) != "Two lines now.\n" {
		t.Fatalf("docs patch: %q %v", b, err)
	}
}

func TestLandPRClosedKeepWorkingReland(t *testing.T) {
	r, srv, _ := prRig(t, false)
	r.packet("")
	r.ready()
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	srv.Set(1, "closed")
	if err := r.env.Poll(r.m); err != nil {
		t.Fatal(err)
	}
	if r.m.Land.State != "unmerged" || !strings.HasPrefix(r.notes[len(r.notes)-1], "unmerged:") {
		t.Fatalf("state %+v notes %v", r.m.Land, r.notes)
	}
	if err := r.env.Keep(r.m, "reviewer: rename two to 2"); err != nil {
		t.Fatal(err)
	}
	if r.m.Land.State != "working" || !strings.Contains(r.handbacks[0], "rename two to 2") {
		t.Fatalf("keep: %+v %v", r.m.Land, r.handbacks)
	}
	write(t, filepath.Join(r.wt(), "app.txt"), "one\n2\n")
	run(t, r.wt(), "git", "commit", "-qam", "review: 2")
	r.ready()
	out, err := r.env.Land(r.m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "opened") || len(srv.Requests()) != 2 {
		t.Fatalf("closed PR should get a new one: %q %+v", out, srv.Requests())
	}
}

func TestLandPRKeepWorkingUpdatesOpenPR(t *testing.T) {
	r, srv, bare := prRig(t, false)
	r.packet("")
	r.ready()
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.env.Keep(r.m, "add a test"); err != nil {
		t.Fatal(err)
	}
	// The worker rewrites history this time (asked to squash): the push needs the lease.
	write(t, filepath.Join(r.wt(), "app.txt"), "one\ntwo\nthree\n")
	run(t, r.wt(), "git", "commit", "-q", "--amend", "-am", "feat: two and three")
	r.ready()
	out, err := r.env.Land(r.m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated") || len(srv.Requests()) != 1 {
		t.Fatalf("want the same PR updated: %q %+v", out, srv.Requests())
	}
	if run(t, r.p.Root, "git", "--git-dir", bare, "rev-parse", "fm/feat") != run(t, r.p.Root, "git", "rev-parse", "fm/feat") {
		t.Fatal("amended branch not pushed")
	}
}

func TestLandPRConflictGoesBack(t *testing.T) {
	r, srv, bare := prRig(t, false)
	// Someone else changes the same line upstream.
	other := t.TempDir()
	run(t, other, "git", "clone", "-q", "-b", "main", bare, ".")
	write(t, filepath.Join(other, "app.txt"), "one\nTWO\n")
	run(t, other, "git", "commit", "-qam", "upstream change")
	run(t, other, "git", "push", "-q", "origin", "main")
	r.packet("")
	r.ready()
	_, err := r.env.Land(r.m, nil)
	if !errors.Is(err, ErrHandedBack) || !strings.Contains(r.handbacks[0], "app.txt") || len(srv.Requests()) != 0 {
		t.Fatalf("err %v handbacks %v requests %v", err, r.handbacks, srv.Requests())
	}
	if out := run(t, r.p.Root, "git", "--git-dir", bare, "branch", "--list", "fm/feat"); out != "" {
		t.Fatal("pushed despite the conflict")
	}
}

func TestLandPRFork(t *testing.T) {
	r, srv, bare := prRig(t, true)
	r.packet("")
	r.ready()
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	rq := srv.Requests()
	if len(rq) != 1 || rq[0].Project != "upstream-org/rig" || rq[0].HeadOwner != "nick" {
		t.Fatalf("requests = %+v", rq)
	}
	if run(t, r.p.Root, "git", "--git-dir", bare, "rev-parse", "fm/feat") == "" {
		t.Fatal("not pushed to the fork remote")
	}
	srv.Set(1, "merged")
	if err := r.env.Poll(r.m); err != nil || r.m.Land.State != "landed" {
		t.Fatalf("poll: %v %+v", err, r.m.Land)
	}
}

func TestLandPRBadTokenChangesNothing(t *testing.T) {
	r, srv, bare := prRig(t, false)
	r.p.Config.Forges["github.com"] = project.Forge{Kind: "github", API: "http://127.0.0.1:1", TokenCmd: []string{"false"}}
	r.packet("")
	r.ready()
	_, err := r.env.Land(r.m, nil)
	if err == nil || !strings.Contains(err.Error(), "token command") {
		t.Fatalf("err = %v", err)
	}
	if out := run(t, r.p.Root, "git", "--git-dir", bare, "branch", "--list", "fm/feat"); out != "" || len(srv.Requests()) != 0 || r.m.Land.State != "ready" {
		t.Fatal("something changed before the token was checked")
	}
}

func TestLandFollowupsFiledOnceAcrossRetry(t *testing.T) {
	r, srv, _ := prRig(t, false)
	r.packet(`{"title":"a"}`)
	r.ready()
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	r.env.Park(r.m)
	srv.Set(1, "open")
	if _, err := r.env.Land(r.m, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(r.l.Proposals()); n != 1 {
		t.Fatalf("proposals filed %d times", n)
	}
}

func TestRemoteBranch(t *testing.T) {
	m := &ledger.Meta{Name: "feat", Ticket: "ABC-12"}
	for tmpl, want := range map[string]string{"": "fm/feat", "fm/{name}": "fm/feat", "{ticket}-{name}": "ABC-12-feat", "nick/{name}": "nick/feat"} {
		if got, err := RemoteBranch(tmpl, m); err != nil || got != want {
			t.Errorf("%q → %q, %v", tmpl, got, err)
		}
	}
	if _, err := RemoteBranch("{ticket}-{name}", &ledger.Meta{Name: "x"}); err == nil {
		t.Error("missing ticket accepted")
	}
	if _, err := RemoteBranch("{bogus}", m); err == nil {
		t.Error("unknown variable accepted")
	}
	if _, err := RemoteBranch("bad..{name}", m); err == nil {
		t.Error("invalid ref accepted")
	}
}

func TestPacketHostile(t *testing.T) {
	r := newRig(t)
	dir := OutDir(r.l, "feat")
	long := strings.Repeat("x", 100<<10)
	var lines []string
	lines = append(lines, `not json`, `{"title":"\u001b[31mred\u001b[0m $(touch PWNED)","goal":"g","type":"Feature; rm -rf"}`, `{"goal":"no title"}`)
	for i := 0; i < 30; i++ {
		lines = append(lines, `{"title":"t"}`)
	}
	write(t, filepath.Join(dir, "landing.md"), "---\nstatus: ready\n---\n## Title\n\x1b[2Jtitle\x07 one\nsecond line ignored\n## Description\nbody\n")
	write(t, filepath.Join(dir, "followups.jsonl"), strings.Join(lines, "\n")+"\n"+long+"\n")
	pk, err := ReadPacket(r.l, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if pk.Title != "[2Jtitle one" {
		t.Fatalf("title = %q", pk.Title)
	}
	if len(pk.Followups) != maxFollowups {
		t.Fatalf("followups = %d", len(pk.Followups))
	}
	f := pk.Followups[0]
	if strings.ContainsRune(f.Title, 0x1b) || f.Type != "" || f.Mode != "interactive" {
		t.Fatalf("first follow-up = %+v", f)
	}
	if len(pk.Skipped) < 2 {
		t.Fatalf("skipped = %v", pk.Skipped)
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("follow-up text was executed")
	}

	// A symlink (to a secret, say) and a FIFO are refused without hanging.
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "TOKEN=abc")
	os.Remove(filepath.Join(dir, "followups.jsonl"))
	if err := os.Symlink(secret, filepath.Join(dir, "followups.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "docs.patch"), 0o644); err != nil {
		t.Fatal(err)
	}
	pk, err = ReadPacket(r.l, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if len(pk.Followups) != 0 || pk.DocsPatch != nil || len(pk.Skipped) != 2 {
		t.Fatalf("symlink/FIFO: %+v", pk)
	}
	// landing.md itself as a symlink: unreadable, so not ready.
	os.Remove(filepath.Join(dir, "landing.md"))
	os.Symlink(secret, filepath.Join(dir, "landing.md"))
	if _, err := ReadPacket(r.l, "feat"); err == nil {
		t.Fatal("symlinked landing.md accepted")
	}
}

func TestDocsPatchConfined(t *testing.T) {
	docs := filepath.Join(t.TempDir(), "docs")
	for _, bad := range []string{
		"--- a/../outside.md\n+++ b/../outside.md\n@@ -0,0 +1 @@\n+x\n",
		"--- /dev/null\n+++ /etc/passwd\n@@ -0,0 +1 @@\n+x\n",
		"--- /dev/null\n+++ b/.git/hooks/pre-commit\n@@ -0,0 +1 @@\n+x\n",
		"just text\n",
	} {
		if err := applyDocsPatch(docs, []byte(bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
	ok := "--- /dev/null\n+++ b/sub/page.md\n@@ -0,0 +1 @@\n+hello\n"
	if err := applyDocsPatch(docs, []byte(ok)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "sub", "page.md")); string(b) != "hello\n" {
		t.Fatalf("page = %q", b)
	}
	// Same patch again no longer applies: reported, not forced.
	if err := applyDocsPatch(docs, []byte(ok)); err == nil {
		t.Fatal("re-applied patch accepted")
	}
}

func TestDocsPatchInsideRepo(t *testing.T) {
	// docs mode local: the docs folder sits inside the project repo. Paths must stay relative
	// to the docs folder, not the repo root.
	root := t.TempDir()
	run(t, root, "git", "init", "-q")
	docs := filepath.Join(root, "foreman-docs")
	if err := applyDocsPatch(docs, []byte("--- /dev/null\n+++ b/page.md\n@@ -0,0 +1 @@\n+hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(docs, "page.md")); err != nil {
		t.Fatal("patch applied relative to the repo root, not the docs folder")
	}
}
