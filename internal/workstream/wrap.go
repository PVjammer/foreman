package workstream

// Wrapped workers: a profile with `wrapper = [...]` runs its harness inside a sandbox
// launcher. herdr's `agent start` always runs its own canonical executable, so foreman
// instead types a launch script into the worker's pane and waits for herdr to detect the
// harness running under the wrapper. See docs/harness-wrapper.md.
//
// Everything here fails closed: a missing wrapper, a wrapper that exits, or a harness that
// isn't detected in time fails the launch. foreman never falls back to the bare harness.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
)

// WrapTimeout is how long a wrapped harness has to be detected and become ready.
// FOREMAN_WRAP_TIMEOUT (a Go duration) overrides it, for scenario runs.
var WrapTimeout = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("FOREMAN_WRAP_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 90 * time.Second
}()

// wrapPoll is how often the launch is checked; tests shorten it.
var wrapPoll = 500 * time.Millisecond

// UnsetForWrapped are herdr's integration variables. With them, a harness's herdr
// integration (pi's extension, Claude's hooks) reports a session that herdr natively resumes
// after a restart by running the bare harness (`pi --session …`), outside the wrapper.
// Without them herdr detects the harness from the screen, records nothing to resume, and
// foreman restarts the worker through the wrapper instead.
var UnsetForWrapped = []string{"HERDR_ENV", "HERDR_PANE_ID"}

// WrapperEnv is the env contract: what a wrapped worker must be able to reach. The wrapper
// decides how to expose these paths (bind mounts, allow-lists); foreman only names them.
func WrapperEnv(p *project.Project, l *ledger.Ledger, m *ledger.Meta, socket string) map[string]string {
	var wts []string
	for _, wt := range m.Worktrees {
		wts = append(wts, wt.Path)
	}
	wsDir := filepath.Dir(l.ReportPath(m.Name))
	writable := append(append([]string{}, wts...), wsDir)
	if len(wts) == 0 {
		writable = append(writable, m.Cwd) // no worktrees: the worker works in place
	}
	return map[string]string{
		"FOREMAN_WRAPPED":      "1",
		"FOREMAN_PROJECT":      p.Config.Name,
		"FOREMAN_PROJECT_ROOT": p.Root,
		"FOREMAN_WORKSTREAM":   m.Name,
		"FOREMAN_HARNESS":      m.Harness,
		"FOREMAN_CWD":          m.Cwd,
		"FOREMAN_WORKTREES":    strings.Join(wts, ":"),
		"FOREMAN_WS_DIR":       wsDir,
		"FOREMAN_WRITABLE":     strings.Join(dedupe(writable), ":"),
		"FOREMAN_DOCS_DIR":     p.DocsDir(),
		"FOREMAN_HERDR_SOCKET": socket,
	}
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// sq quotes s for a POSIX shell.
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (wr wrapRun) exitPath() string   { return filepath.Join(wr.dir, "wrapper-exit") }
func (wr wrapRun) scriptPath() string { return filepath.Join(wr.dir, "launch.sh") }

type wrapRun struct{ dir string }

// LaunchScript is the sh script typed into the pane. It runs the wrapper as a child (not
// exec) so its exit status is recorded; the harness stays in the pane's foreground process
// group, which is how herdr detects it.
func LaunchScript(argv []string, env map[string]string, unset []string, cwd, exitPath string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Written by foreman for a wrapped worker; rewritten on every launch.\n")
	if len(unset) > 0 {
		b.WriteString("unset " + strings.Join(unset, " ") + "\n")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, sq(env[k]))
	}
	fmt.Fprintf(&b, "rm -f %s\n", sq(exitPath))
	fmt.Fprintf(&b, "cd %s || { echo 1 > %s; exit 1; }\n", sq(cwd), sq(exitPath))
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = sq(a)
	}
	b.WriteString(strings.Join(q, " ") + "\n")
	b.WriteString("rc=$?\n")
	fmt.Fprintf(&b, "echo \"$rc\" > %s\n", sq(exitPath))
	b.WriteString("echo \"[foreman] the wrapper exited with status $rc. This worker is stopped; foreman never runs it unwrapped.\"\n")
	b.WriteString("exit \"$rc\"\n")
	return b.String()
}

// WrappedArgv resolves the wrapper on PATH and appends the harness command line.
func WrappedArgv(wrapper []string, harness string, args []string) ([]string, error) {
	if len(wrapper) == 0 || strings.TrimSpace(wrapper[0]) == "" {
		return nil, errors.New("no wrapper")
	}
	bin, err := exec.LookPath(wrapper[0])
	if err != nil {
		return nil, fmt.Errorf("wrapper %q not found on PATH: %w", wrapper[0], err)
	}
	out := append([]string{bin}, wrapper[1:]...)
	out = append(out, harness)
	return append(out, args...), nil
}

// startWrapped launches m's harness under its wrapper in m's pane, names the detected agent
// and sends the kickoff (or parks it, as for an unwrapped worker). On any failure it records
// a failed event and an inbox item, interrupts whatever the wrapper left running, and returns
// the error.
func startWrapped(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, prompt string) error {
	err := launchWrapped(p, l, h, m)
	if err != nil {
		failWrapped(p, l, h, m, err)
		return err
	}
	name := space.AgentName(p, m.Name)
	if err := h.Rename(m.PaneID, name); err != nil {
		failWrapped(p, l, h, m, fmt.Errorf("name the wrapped agent: %w", err))
		return err
	}
	// Like `agent start`, wait for readiness; a dialog or a slow load parks the kickoff for
	// the watcher, exactly as for an unwrapped worker.
	if waitReady(h, m.PaneID, WrapTimeout) {
		if err := h.Prompt(m.PaneID, prompt); err == nil {
			return nil
		}
	}
	return l.SetKickoff(m.Name, prompt)
}

func launchWrapped(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta) error {
	argv, err := WrappedArgv(m.Wrapper, m.Harness, m.Args)
	if err != nil {
		return err
	}
	if pi, err := h.ProcessInfo(m.PaneID); err != nil {
		return fmt.Errorf("check pane %s: %w", m.PaneID, err)
	} else if !pi.AtPrompt() {
		return fmt.Errorf("pane %s is busy (not at its shell prompt); refusing to type the wrapper into it", m.PaneID)
	}
	wr := wrapRun{dir: filepath.Dir(l.ReportPath(m.Name))}
	if strings.ContainsAny(wr.scriptPath(), `'\`+"\n") {
		return fmt.Errorf("can't launch from %s: the path has a quote or backslash", wr.scriptPath())
	}
	env := WrapperEnv(p, l, m, herdr.SocketPath(p.Config.Herdr.Session))
	script := LaunchScript(argv, env, UnsetForWrapped, m.Cwd, wr.exitPath())
	if err := os.WriteFile(wr.scriptPath(), []byte(script), 0o755); err != nil {
		return err
	}
	_ = os.Remove(wr.exitPath())
	// Typed into the user's shell; one single-quoted path works in sh, bash, zsh and fish.
	if err := h.RunIn(m.PaneID, "sh "+sq(wr.scriptPath())); err != nil {
		return err
	}
	return waitDetected(h, m, wr, WrapTimeout)
}

// waitDetected polls until herdr sees m.Harness in m's pane. A wrapper that exits, or a
// different agent showing up, fails at once; nothing within d fails with a timeout.
func waitDetected(h *herdr.Client, m *ledger.Meta, wr wrapRun, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if b, err := os.ReadFile(wr.exitPath()); err == nil {
			rc, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return fmt.Errorf("the wrapper exited with status %d before %s was detected%s", rc, m.Harness, tail(h, m.PaneID))
		}
		if agents, err := h.Agents(); err == nil {
			for _, a := range agents {
				if a.PaneID != m.PaneID {
					continue
				}
				if a.Kind == m.Harness {
					return nil
				}
				if a.Kind != "" {
					stopPane(h, m.PaneID)
					return fmt.Errorf("herdr detected %s in the pane, not %s", a.Kind, m.Harness)
				}
			}
		}
		if time.Now().After(deadline) {
			stopPane(h, m.PaneID) // an undetected harness must not keep running unsupervised
			return fmt.Errorf("%s wasn't detected under the wrapper within %s (if the wrapper starts a new session or a container, herdr can't see the harness)%s",
				m.Harness, d, tail(h, m.PaneID))
		}
		time.Sleep(wrapPoll)
	}
}

// stopPane kills everything running under the pane's shell. Ctrl+C isn't enough: it reaches
// only the foreground process group, and a harness the wrapper put in a new session (the
// reason it went undetected) survives it, and even the herdr server stopping.
func stopPane(h *herdr.Client, pane string) {
	pi, err := h.ProcessInfo(pane)
	if err != nil || pi.ShellPID == 0 {
		_ = h.PaneKeys(pane, "ctrl+c")
		return
	}
	killDescendants(pi.ShellPID)
}

// killDescendants sends SIGTERM, then SIGKILL, to every descendant of pid (not pid itself).
func killDescendants(pid int) {
	tree := descendants(pid)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		alive := 0
		for _, c := range tree {
			if syscall.Kill(c, sig) == nil {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		time.Sleep(time.Second)
	}
}

func descendants(pid int) []int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var all []int
	for _, f := range strings.Fields(string(out)) {
		if c, err := strconv.Atoi(f); err == nil {
			all = append(all, c)
			all = append(all, descendants(c)...)
		}
	}
	return all
}

func waitReady(h *herdr.Client, pane string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if agents, err := h.Agents(); err == nil {
			for _, a := range agents {
				if a.PaneID == pane {
					switch a.Status {
					case "idle", "done":
						return true
					case "blocked":
						return false
					}
				}
			}
		}
		time.Sleep(wrapPoll)
	}
	return false
}

func tail(h *herdr.Client, pane string) string {
	out, err := h.PaneRead(pane, 6)
	out = strings.TrimSpace(out)
	if err != nil || out == "" {
		return ""
	}
	return "; pane output:\n" + out
}

// failWrapped makes a failed launch visible: an event, an inbox item and a toast.
func failWrapped(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, err error) {
	msg := "wrapped launch failed: " + firstLine(err.Error())
	_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "failed", Summary: msg})
	_ = l.AddItem(ledger.Item{WS: m.Name, Kind: "blocked", Summary: msg})
	_ = h.Notify(p.Config.Name+" · "+m.Name+"  ✖ wrapper failed", firstLine(err.Error()), "none")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// paneAtPrompt reports whether a pane's shell is free to type into.
func paneAtPrompt(h *herdr.Client, pane string) bool {
	pi, err := h.ProcessInfo(pane)
	return err == nil && pi.AtPrompt()
}
