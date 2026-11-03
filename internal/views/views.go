// Package views shows sessions without moving them. Every session (orchestrator, workstream,
// scratch, terminal) lives in its own pane; a view is a live attach to that pane, either
// floating (a herdr popup) or docked (a split beside the orchestrator). Closing a view
// (ctrl+b q) leaves the session running. Only one view can own a terminal at a time.
//
// It also owns the outbox: prompts waiting for an agent to become idle, which the watcher
// delivers (workstream requests to a busy orchestrator, scratch's first prompt).
package views

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
)

const (
	Orchestrator = "orchestrator"
	Scratch      = space.ScratchTab
	Terminal     = space.TerminalTab
)

// Target is a resolved session: the pane it lives in and whether an agent runs there.
type Target struct {
	Name  string
	Pane  herdr.Pane
	Agent bool
}

func open(p *project.Project, h *herdr.Client) (*space.Space, error) {
	sp, err := space.Find(p, h)
	if err != nil {
		return nil, err
	}
	if sp.ID == "" {
		return nil, fmt.Errorf("%s isn't open (run `foreman up`)", p.Config.Name)
	}
	return sp, nil
}

func paneByID(h *herdr.Client, ws, id string) (herdr.Pane, bool) {
	panes, err := h.Panes(ws)
	if err != nil {
		return herdr.Pane{}, false
	}
	for _, pn := range panes {
		if pn.PaneID == id {
			return pn, true
		}
	}
	return herdr.Pane{}, false
}

// Resolve finds (creating scratch or terminal if needed) the session a view should show.
func Resolve(p *project.Project, h *herdr.Client, name string) (Target, error) {
	sp, err := open(p, h)
	if err != nil {
		return Target{}, err
	}
	switch name {
	case Orchestrator, "orch":
		id, err := sp.OrchestratorPane()
		if err != nil {
			return Target{}, err
		}
		pn, _ := paneByID(h, sp.ID, id)
		return Target{Orchestrator, pn, true}, nil
	case Scratch:
		pn, err := EnsureScratch(p, h, sp)
		return Target{Scratch, pn, true}, err
	case Terminal:
		pn, _, err := sp.EnsureTab(Terminal, p.Root)
		return Target{Terminal, pn, false}, err
	}
	m, err := ledger.New(p).Meta(name)
	if err != nil || m.Closed {
		return Target{}, fmt.Errorf("no open workstream %q", name)
	}
	pane, err := sp.WorkerPane(m)
	if err != nil {
		return Target{}, err
	}
	pn, _ := paneByID(h, sp.ID, pane)
	return Target{name, pn, true}, nil
}

// AttachArgv is the herdr command line that shows t live in the current terminal. Agents are
// attached by pane id (stable across restarts); shells by terminal id, looked up just now
// because herdr issues new terminal ids on every restart.
func AttachArgv(h *herdr.Client, t Target) []string {
	if t.Agent {
		return h.Argv("agent", "attach", t.Pane.PaneID, "--takeover")
	}
	return h.Argv("terminal", "attach", t.Pane.TerminalID, "--takeover")
}

// Exec replaces this process with the attach. Used by popups and dock panes.
func Exec(h *herdr.Client, t Target) error {
	argv := AttachArgv(h, t)
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, argv, os.Environ())
}

// ---- scratch ----

func scratchKickoff(p *project.Project) string {
	return fmt.Sprintf("You are the scratch session for the project at %s (%q). The user opens you in a floating window for quick, throwaway questions: where is something, how do I do X, what is a credential or setting called. "+
		"Now, read the root AGENTS.md / CLAUDE.md and skim the project docs folder (%s, if it exists) so you have the project's context, then say \"ready\" and wait. "+
		"Answer briefly and concretely, with paths. Don't change files or run anything that changes state unless the user explicitly asks. "+
		"Nothing here is tracked. If a question turns into real work, offer to hand it to the orchestrator as a workstream request: `foreman request \"<what to do>\"`.",
		p.Root, p.Config.Name, p.DocsDir())
}

// EnsureScratch returns the scratch pane, starting its agent the first time.
func EnsureScratch(p *project.Project, h *herdr.Client, sp *space.Space) (herdr.Pane, error) {
	pn, _, err := sp.EnsureTab(Scratch, p.Root)
	if err != nil {
		return pn, err
	}
	agents, _ := h.Agents()
	for _, a := range agents {
		if a.PaneID == pn.PaneID {
			return pn, nil
		}
	}
	_, pr := p.Config.Resolve(p.Config.Scratch.Profile)
	if pr.Wrapped() {
		return pn, fmt.Errorf("scratch profile %s has a wrapper; wrappers are for workers only", p.Config.Scratch.Profile)
	}
	time.Sleep(300 * time.Millisecond) // let a fresh tab's shell reach its prompt
	if err := h.StartAgent(space.AgentName(p, Scratch), pr.Harness, pn.PaneID, pr.Args); err != nil &&
		!strings.Contains(err.Error(), "agent_not_ready") && !strings.Contains(err.Error(), "agent_blocked") {
		return pn, err
	}
	return pn, Send(p, h, pn.PaneID, scratchKickoff(p))
}

// ClearScratch ends the scratch session; the next open starts a fresh one.
func ClearScratch(p *project.Project, h *herdr.Client) error {
	sp, err := open(p, h)
	if err != nil {
		return err
	}
	_, pn, err := sp.Tab(Scratch)
	if err != nil || pn.PaneID == "" {
		return err
	}
	dropOutbox(p, pn.PaneID)
	return h.ClosePane(pn.PaneID)
}

// ---- docks ----

type Dock struct {
	Target string `json:"target"`
	Pane   string `json:"pane"`
}

func docksPath(p *project.Project) string { return p.Path("logs", "docks.json") }

func Docks(p *project.Project) []Dock {
	var d []Dock
	if b, err := os.ReadFile(docksPath(p)); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

func saveDocks(p *project.Project, d []Dock) error {
	b, _ := json.MarshalIndent(d, "", "  ")
	return os.WriteFile(docksPath(p), b, 0o644)
}

// liveDocks drops records whose pane is gone (closed with ctrl+b q, or by hand).
func liveDocks(p *project.Project, h *herdr.Client, ws string) []Dock {
	var keep []Dock
	for _, d := range Docks(p) {
		if _, ok := paneByID(h, ws, d.Pane); ok {
			keep = append(keep, d)
		}
	}
	return keep
}

// DockedPane returns the dock pane showing target, if any.
func DockedPane(p *project.Project, h *herdr.Client, target string) string {
	sp, err := open(p, h)
	if err != nil {
		return ""
	}
	for _, d := range liveDocks(p, h, sp.ID) {
		if d.Target == target {
			return d.Pane
		}
	}
	return ""
}

// viewCommand is what a dock pane runs: exec, so closing the view closes the pane.
func viewCommand(target string) string {
	exe, _ := os.Executable()
	return fmt.Sprintf("exec '%s' view --docked '%s'", exe, target)
}

// DockIt opens a split beside the orchestrator showing target. Docking something already
// docked is a no-op that returns its pane.
func DockIt(p *project.Project, h *herdr.Client, target string) (string, error) {
	sp, err := open(p, h)
	if err != nil {
		return "", err
	}
	if target == "orch" {
		target = Orchestrator
	}
	if target == Orchestrator {
		return "", fmt.Errorf("the orchestrator is what things dock beside")
	}
	docks := liveDocks(p, h, sp.ID)
	for _, d := range docks {
		if d.Target == target {
			return d.Pane, nil
		}
	}
	if _, err := Resolve(p, h, target); err != nil { // creates scratch/term on first use
		return "", err
	}
	orch, err := sp.OrchestratorPane()
	if err != nil {
		return "", err
	}
	// The first dock goes right of the orchestrator; further docks stack under the last one.
	at, dir := orch, "right"
	if len(docks) > 0 {
		at, dir = docks[len(docks)-1].Pane, "down"
	}
	pane, err := h.Split(at, dir, p.Root)
	if err != nil {
		return "", err
	}
	time.Sleep(300 * time.Millisecond)
	if err := h.RunIn(pane, viewCommand(target)); err != nil {
		return "", err
	}
	docks = append(docks, Dock{Target: target, Pane: pane})
	return pane, saveDocks(p, docks)
}

// Undock closes target's dock pane (the session keeps running).
func Undock(p *project.Project, h *herdr.Client, target string) error {
	pane := DockedPane(p, h, target)
	if pane == "" {
		return fmt.Errorf("%s isn't docked", target)
	}
	return h.ClosePane(pane)
}

// Redock reopens docks after a herdr restart: herdr restores the dock panes' layout but runs
// a plain shell in them, so the view command has to be started again.
func Redock(p *project.Project, h *herdr.Client) {
	sp, err := open(p, h)
	if err != nil {
		return
	}
	docks := liveDocks(p, h, sp.ID)
	for _, d := range docks {
		_ = h.RunIn(d.Pane, viewCommand(d.Target))
	}
	_ = saveDocks(p, docks)
}

// Choices lists what can be viewed or docked, for pickers.
func Choices(p *project.Project) []string {
	out := []string{Scratch, Terminal}
	var ws []string
	for _, m := range ledger.New(p).List(false) {
		ws = append(ws, m.Name)
	}
	sort.Strings(ws)
	return append(out, ws...)
}

// ---- outbox ----

// The outbox holds prompts for agents that aren't ready: one file per prompt, named by pane.
// Writers are CLI commands; the watcher delivers and deletes them.

func outboxDir(p *project.Project) string { return p.Path("outbox") }

// Send prompts pane now if its agent is ready, and otherwise leaves it in the outbox for the
// watcher. queued reports which happened.
func Send(p *project.Project, h *herdr.Client, pane, text string) error {
	_, err := SendOrQueue(p, h, pane, text)
	return err
}

func SendOrQueue(p *project.Project, h *herdr.Client, pane, text string) (queued bool, err error) {
	if agents, err := h.Agents(); err == nil {
		for _, a := range agents {
			if a.PaneID == pane && (a.Status == "idle" || a.Status == "done") {
				if h.Prompt(pane, text) == nil {
					return false, nil
				}
			}
		}
	}
	if err := os.MkdirAll(outboxDir(p), 0o755); err != nil {
		return false, err
	}
	name := fmt.Sprintf("%d_%s.txt", time.Now().UnixNano(), strings.ReplaceAll(pane, ":", "_"))
	return true, os.WriteFile(filepath.Join(outboxDir(p), name), []byte(text), 0o644)
}

type Queued struct {
	Path, Pane, Text string
}

// Outbox lists queued prompts, oldest first.
func Outbox(p *project.Project) []Queued {
	entries, _ := os.ReadDir(outboxDir(p))
	var out []Queued
	for _, e := range entries {
		_, rest, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".txt"), "_")
		if !ok {
			continue
		}
		path := filepath.Join(outboxDir(p), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, Queued{Path: path, Pane: strings.Replace(rest, "_", ":", 1), Text: string(b)})
	}
	return out // ReadDir sorts by name, and names start with a timestamp
}

func dropOutbox(p *project.Project, pane string) {
	for _, q := range Outbox(p) {
		if q.Pane == pane {
			_ = os.Remove(q.Path)
		}
	}
}
