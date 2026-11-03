package workstream

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

func wrapFixture(t *testing.T) (*project.Project, *ledger.Ledger, *ledger.Meta) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, project.Dir), 0o755)
	os.WriteFile(filepath.Join(root, project.Dir, "config.toml"), []byte("name = \"proj\"\n"), 0o644)
	p, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	l := &ledger.Ledger{P: p}
	ws := p.WorkstreamDir("boxed")
	os.MkdirAll(ws, 0o755)
	m := &ledger.Meta{Name: "boxed", Harness: "pi", Args: []string{"--model", "it's"}, Cwd: ws,
		Wrapper:   []string{"agentbox"},
		Worktrees: []ledger.Worktree{{Repo: ".", Path: filepath.Join(ws, "root")}, {Repo: "api", Path: filepath.Join(ws, "api")}}}
	return p, l, m
}

func TestWrapperEnvContract(t *testing.T) {
	p, l, m := wrapFixture(t)
	env := WrapperEnv(p, l, m, "/run/herdr.sock")
	ws := p.WorkstreamDir("boxed")
	want := map[string]string{
		"FOREMAN_WRAPPED":      "1",
		"FOREMAN_PROJECT":      "proj",
		"FOREMAN_PROJECT_ROOT": p.Root,
		"FOREMAN_WORKSTREAM":   "boxed",
		"FOREMAN_HARNESS":      "pi",
		"FOREMAN_CWD":          ws,
		"FOREMAN_WORKTREES":    filepath.Join(ws, "root") + ":" + filepath.Join(ws, "api"),
		"FOREMAN_WS_DIR":       ws,
		"FOREMAN_WRITABLE":     filepath.Join(ws, "root") + ":" + filepath.Join(ws, "api") + ":" + ws,
		"FOREMAN_DOCS_DIR":     p.DocsDir(),
		"FOREMAN_HERDR_SOCKET": "/run/herdr.sock",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("unexpected keys: %v", env)
	}
	// No worktrees: the worker writes where it runs.
	m.Worktrees, m.Cwd = nil, p.Root
	env = WrapperEnv(p, l, m, "/s")
	if env["FOREMAN_WORKTREES"] != "" || env["FOREMAN_WRITABLE"] != ws+":"+p.Root {
		t.Errorf("no worktrees: %q / %q", env["FOREMAN_WORKTREES"], env["FOREMAN_WRITABLE"])
	}
}

func TestWrappedArgvFailsClosedWhenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := WrappedArgv([]string{"agentbox"}, "pi", nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing wrapper: %v", err)
	}
	if _, err := WrappedArgv(nil, "pi", nil); err == nil {
		t.Fatal("no wrapper should be an error")
	}
}

// TestLaunchScriptRunsDummyWrapper runs the generated script for real: a dummy wrapper logs
// its env and execs a fake harness, which records its argv.
func TestLaunchScriptRunsDummyWrapper(t *testing.T) {
	p, l, m := wrapFixture(t)
	bin := t.TempDir()
	logf := filepath.Join(bin, "wrapper.env")
	argvf := filepath.Join(bin, "harness.argv")
	os.WriteFile(filepath.Join(bin, "dummybox"), []byte("#!/bin/sh\nenv > '"+logf+"'\nwhile [ \"$1\" != pi ]; do shift; done\nexec \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argvf+"'\nexit 3\n"), 0o755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	t.Setenv("HERDR_SOCKET_PATH", "/keep/me")
	m.Wrapper = []string{"dummybox", "--level", "1"}
	argv, err := WrappedArgv(m.Wrapper, m.Harness, m.Args)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != filepath.Join(bin, "dummybox") {
		t.Fatalf("wrapper not resolved to an absolute path: %v", argv)
	}
	wr := wrapRun{dir: p.WorkstreamDir("boxed")}
	script := LaunchScript(argv, WrapperEnv(p, l, m, "/run/herdr.sock"), UnsetForWrapped, m.Cwd, wr.exitPath())
	os.WriteFile(wr.scriptPath(), []byte(script), 0o755)
	out, err := exec.Command("sh", wr.scriptPath()).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("want the harness's exit status 3, got %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(wr.exitPath()); strings.TrimSpace(string(b)) != "3" {
		t.Errorf("exit file: %q", b)
	}
	if !strings.Contains(string(out), "never runs it unwrapped") {
		t.Errorf("no exit notice in pane output: %s", out)
	}
	envb, _ := os.ReadFile(logf)
	env := string(envb)
	for _, want := range []string{"FOREMAN_WORKSTREAM=boxed", "FOREMAN_HERDR_SOCKET=/run/herdr.sock", "FOREMAN_WS_DIR=" + wr.dir, "HERDR_SOCKET_PATH=/keep/me", "PWD=" + m.Cwd} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("wrapper env lacks %s", want)
		}
	}
	for _, gone := range []string{"HERDR_ENV=", "HERDR_PANE_ID="} {
		if strings.Contains(env, "\n"+gone) || strings.HasPrefix(env, gone) {
			t.Errorf("wrapper env still has %s (herdr would natively resume the bare harness)", gone)
		}
	}
	if b, _ := os.ReadFile(argvf); string(b) != "--model\nit's\n" {
		t.Errorf("harness argv: %q", b)
	}
}
