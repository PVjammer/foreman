// Package workstream creates, restarts, closes out and closes workstreams: one worker agent
// in the project's herdr workspace, with a brief going in and a report coming out. Types
// with worktrees get a git worktree per repo they touch, all under .foreman/ws/<name>/.
package workstream

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PVjammer/foreman/internal/assets"
	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/types"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type Spec struct {
	Name, Type, Profile, Goal, Ticket string
	Repos                             []string
	Focus                             bool
	NoLanding                         bool // close out as before even if the project lands
}

func Create(p *project.Project, l *ledger.Ledger, h *herdr.Client, s Spec) (*ledger.Meta, error) {
	if !nameRe.MatchString(s.Name) {
		return nil, fmt.Errorf("name %q must match [a-z][a-z0-9_-]{0,31}", s.Name)
	}
	for _, r := range space.Reserved {
		if s.Name == r {
			return nil, fmt.Errorf("%q is reserved", s.Name)
		}
	}
	if _, err := l.Meta(s.Name); err == nil {
		return nil, fmt.Errorf("workstream %q already exists (closed ones live in .foreman/archive)", s.Name)
	}
	if s.Type == "" {
		s.Type = p.Config.Workers.Type
	}
	t, err := types.Get(p, s.Type)
	if err != nil {
		return nil, err
	}
	// Profile precedence: flag > type > project/global default.
	prof := s.Profile
	if prof == "" {
		prof = t.Profile
	}
	if prof == "" {
		prof = p.Config.WorkerProfile()
	}
	pname, pr := p.Config.Resolve(prof)

	dir := p.WorkstreamDir(s.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &ledger.Meta{Name: s.Name, Kind: t.Name, Profile: pname, Harness: pr.Harness, Args: pr.Args,
		Goal: s.Goal, Ticket: s.Ticket, Repos: s.Repos, Cwd: p.Root, Created: time.Now(),
		Landing: p.Config.Landing.Enabled && !s.NoLanding}

	// Code-changing types are isolated in worktrees; the rest run at the project root.
	if t.Worktrees && len(s.Repos) > 0 {
		m.Cwd = dir
		for _, repo := range s.Repos {
			wt, err := addWorktree(p.Root, repo, dir, s.Name)
			if err != nil {
				return nil, err
			}
			m.Worktrees = append(m.Worktrees, wt)
		}
	}
	if err := writeBrief(p, l, m, t); err != nil {
		return nil, err
	}
	if err := launch(p, l, h, m, s.Focus, kickoff(p, l, m, false)); err != nil {
		return m, err
	}
	_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "created", Summary: m.Goal})
	return m, nil
}

func addWorktree(root, repo, wsDir, name string) (ledger.Worktree, error) {
	repoPath := filepath.Join(root, repo)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return ledger.Worktree{}, fmt.Errorf("%s is not a git repo", repo)
	}
	sub := repo
	if repo == "." {
		sub = "root"
	}
	path := filepath.Join(wsDir, sub)
	branch := "fm/" + name
	args := []string{"-C", repoPath, "worktree", "add", path, "-b", branch}
	if exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", branch).Run() == nil {
		args = []string{"-C", repoPath, "worktree", "add", path, branch}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return ledger.Worktree{}, fmt.Errorf("git worktree add (%s): %s", repo, strings.TrimSpace(string(out)))
	}
	return ledger.Worktree{Repo: repo, Path: path, Branch: branch}, nil
}

func render(name string, data any) (string, error) {
	var buf bytes.Buffer
	err := assets.Templates.ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

func writeBrief(p *project.Project, l *ledger.Ledger, m *ledger.Meta, t *types.Type) error {
	s, err := render("brief.md", map[string]any{
		"M": m, "T": t, "Root": p.Root, "Report": l.ReportPath(m.Name), "Docs": p.DocsDir(),
		"Landing": landingData(p, l, m),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(l.BriefPath(m.Name), []byte(s), 0o644)
}

func kickoff(p *project.Project, l *ledger.Ledger, m *ledger.Meta, resume bool) string {
	verb := "You are a worker on workstream %q."
	if resume {
		verb = "You are resuming workstream %q after a restart. Your previous conversation is gone and any command you were running was interrupted, so rely on the brief, your report so far, and the state of the files. If your report already says done, just check it is still accurate and stop."
	}
	return fmt.Sprintf("[foreman → %s] "+verb+" Read your brief at %s and do the work it describes. "+
		"Project context is in %s (AGENTS.md / CLAUDE.md). "+
		"When you finish, or when you need a decision from the user, write %s in the format the brief gives, then stop and wait.",
		m.Name, m.Name, l.BriefPath(m.Name), p.Root, l.ReportPath(m.Name))
}

// launch opens a pane for the worker in the project's workspace, starts it and sends the kickoff.
func launch(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, focus bool, prompt string) error {
	sp, _, err := space.Ensure(p, h, false)
	if err != nil {
		return err
	}
	tab, pane, err := sp.NewWorkerPane(m.Name, m.Cwd, focus)
	if err != nil {
		return err
	}
	m.WorkspaceID, m.TabID, m.PaneID = sp.ID, tab, pane
	if err := l.SaveMeta(m); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond) // let the shell reach its prompt
	return startAndKickoff(p, l, h, m, prompt)
}

// startAndKickoff starts the agent and sends its first prompt. If the agent comes up behind a
// dialog (e.g. Claude's folder-trust prompt) the kickoff is parked in a file and the watcher
// delivers it once the agent is idle; the dialog itself surfaces as a blocked event.
func startAndKickoff(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, prompt string) error {
	err := h.StartAgent(space.AgentName(p, m.Name), m.Harness, m.PaneID, m.Args)
	if err == nil {
		err = h.Prompt(m.PaneID, prompt)
	}
	if err != nil && (strings.Contains(err.Error(), "agent_not_ready") || strings.Contains(err.Error(), "agent_blocked")) {
		return l.SetKickoff(m.Name, prompt)
	}
	return err
}

// Restart brings a workstream back when herdr could not resume its agent natively: reuse its
// pane if it is still there, otherwise open a new one.
func Restart(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta) error {
	prompt := kickoff(p, l, m, true)
	if m.Phase == "closeout" || m.Phase == "applying" {
		prompt += " You were in the middle of closing out: read " + l.CloseoutPath(m.Name) + " and continue from there."
	}
	if m.Phase == "landing" {
		return nil // ready to land, in review or parked: there's no worker session to bring back
	}
	// Reuse the old pane only if it is still this workstream's; a pane taken by something
	// else (the orchestrator, once) is left alone and the worker gets a new one.
	if pane, err := space.WorkerPane(p, h, m); err == nil && pane == m.PaneID {
		return startAndKickoff(p, l, h, m, prompt)
	}
	return launch(p, l, h, m, false, prompt)
}

// Finish starts closeout: the worker drafts closeout.md (final report, doc updates, tracker
// updates, follow-ups) and waits. The user approves it from the inbox or `ws approve`.
func Finish(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta) error {
	if m.Phase != "" {
		return fmt.Errorf("%s is already closing out (phase %s)", m.Name, m.Phase)
	}
	t, err := types.Get(p, m.Kind)
	if err != nil {
		return err
	}
	var prompt string
	if landingData(p, l, m) != nil {
		// Landing workstreams prepare their packet instead; the watcher sees it when the
		// report says done, and the user lands from the inbox.
		if prompt, err = landingPrompt(p, l, m); err != nil {
			return err
		}
		pane, err := space.WorkerPane(p, h, m)
		if err != nil {
			return fmt.Errorf("can't ask %s to prepare landing: %w", m.Name, err)
		}
		if err := h.Prompt(pane, prompt); err != nil {
			return fmt.Errorf("send landing prompt (is the worker running? `foreman ws resume %s`): %w", m.Name, err)
		}
		_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "finishing", Summary: "prepare landing"})
		return nil
	}
	prompt, err = render("closeout.md", map[string]any{
		"M": m, "T": t, "Closeout": l.CloseoutPath(m.Name), "Docs": p.DocsDir(), "Tracker": p.Config.Tracker.Kind,
	})
	if err != nil {
		return err
	}
	pane, err := space.WorkerPane(p, h, m)
	if err != nil {
		return fmt.Errorf("can't ask %s to close out: %w", m.Name, err)
	}
	if err := h.Prompt(pane, prompt); err != nil {
		return fmt.Errorf("send closeout prompt (is the worker running? `foreman ws resume %s`): %w", m.Name, err)
	}
	m.Phase = "closeout"
	_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "finishing"})
	return l.SaveMeta(m)
}

// Approve tells the worker to apply its closeout. note is optional feedback to apply with it.
func Approve(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, note string) error {
	if m.Phase != "closeout" {
		return fmt.Errorf("%s has no closeout waiting for approval (run `foreman ws finish %s` first)", m.Name, m.Name)
	}
	if _, st, _ := l.Closeout(m.Name); st != "proposed" {
		return fmt.Errorf("%s hasn't written its closeout yet (%s)", m.Name, l.CloseoutPath(m.Name))
	}
	if note != "" {
		note = " with this change from the user: " + note
	}
	pane, err := space.WorkerPane(p, h, m)
	if err != nil {
		return fmt.Errorf("can't send the approval to %s: %w", m.Name, err)
	}
	prompt := fmt.Sprintf("[foreman → %s] Your closeout was approved%s. Now: "+
		"1) apply the Doc updates to %s (create the folder, and a README.md index there, if missing); don't commit the docs, the user reviews them. "+
		"2) apply only the listed Tracker updates, following .foreman/tracker.md. "+
		"3) for each follow-up without a ticket run `foreman backlog add \"<text>\" --ws %s`. "+
		"4) commit any remaining work on your branches. "+
		"5) set `status: applied` in the frontmatter of %s and stop. foreman will then close this session.",
		m.Name, note, p.DocsDir(), m.Name, l.CloseoutPath(m.Name))
	if err := h.Prompt(pane, prompt); err != nil {
		return err
	}
	m.Phase = "applying"
	_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "approved", Summary: note})
	return l.SaveMeta(m)
}

// disposable are untracked paths a worktree can be removed with: caches and build output
// that tools regenerate (test runs leave these behind all the time).
var disposable = []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "node_modules", ".venv", ".DS_Store", ".coverage"}

// uncommitted lists a worktree's changes that would be lost: modified or staged tracked
// files, and untracked files other than disposable caches.
func uncommitted(path string) []string {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		return []string{"git status failed: " + err.Error()}
	}
	var keep []string
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(ln) < 4 {
			continue
		}
		file := strings.TrimSuffix(ln[3:], "/")
		if strings.HasPrefix(ln, "?? ") && isDisposable(file) {
			continue
		}
		keep = append(keep, file)
	}
	return keep
}

func isDisposable(file string) bool {
	if strings.HasSuffix(file, ".pyc") {
		return true
	}
	for _, part := range strings.Split(file, "/") {
		for _, d := range disposable {
			if part == d {
				return true
			}
		}
	}
	return false
}

// Close ends a workstream: removes its worktrees (branches are kept), closes its pane, marks
// it closed and moves it to the archive. If a worktree has uncommitted changes nothing is
// forced: the error says so and the workstream stays open. keepWorktrees skips removal (the
// folder then stays in ws/, since moving it would break the worktrees).
func Close(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, keepWorktrees bool) error {
	if !keepWorktrees {
		for _, wt := range m.Worktrees {
			if _, err := os.Stat(wt.Path); err != nil {
				continue
			}
			if blocking := uncommitted(wt.Path); len(blocking) > 0 {
				return fmt.Errorf("worktree %s has uncommitted changes (commit or discard them, then `foreman ws close %s`): %s",
					wt.Path, m.Name, strings.Join(blocking, ", "))
			}
			// Only regenerable caches can be left at this point, so --force loses nothing.
			out, err := exec.Command("git", "-C", filepath.Join(p.Root, wt.Repo), "worktree", "remove", "--force", wt.Path).CombinedOutput()
			if err != nil {
				return fmt.Errorf("remove worktree %s: %s", wt.Path, strings.TrimSpace(string(out)))
			}
		}
	}
	// Close the pane only if it is still this workstream's.
	if pane, err := space.WorkerPane(p, h, m); err == nil {
		_ = h.ClosePane(pane)
	}
	now := time.Now()
	m.Closed, m.ClosedAt = true, &now
	if err := l.SaveMeta(m); err != nil {
		return err
	}
	_ = l.AddEvent(ledger.Event{WS: m.Name, Kind: "closed"})
	for _, it := range l.Pending() { // nothing left to act on
		if it.WS == m.Name {
			_ = l.Ack(it.ID)
		}
	}
	if keepWorktrees && len(m.Worktrees) > 0 {
		return nil
	}
	return l.Archive(m.Name)
}
