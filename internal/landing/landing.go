// Package landing ends a workstream by landing its work: the worker prepares a packet
// (commits on its branch, landing.md, follow-ups, a docs patch) and exits; the user presses
// Land once; foreman then fast-forwards the base branch (personal projects) or pushes the
// branch and opens a pull/merge request (forge projects), files the follow-ups, and closes the
// workstream once the work is merged. Conflicts and failing gates go back to a worker.
//
// Landing is opt-in ([landing] enabled = true, per repo policies). Workstreams without it
// close out as before (workstream.Finish / Approve). See docs/design.md, D11.
//
// States (ledger.LandState.State):
//
//	working → ready → in-review (PR) → landed → closed and archived
//	                ↘ landed (ff)
//	in-review → unmerged (PR closed without merging) → park | close | keep working
//	ready | unmerged → parked (open, idle, no nudges) → land | keep working | close
package landing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PVjammer/foreman/internal/forge"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

// Env is what landing needs from the rest of foreman. The hooks keep this package free of
// herdr, so it can be tested and scripted without a running session.
type Env struct {
	P *project.Project
	L *ledger.Ledger
	// Handback restarts a worker on the workstream with a message (conflicts, failing gate,
	// review comments). Required for Land and Keep.
	Handback func(m *ledger.Meta, msg string) error
	// EndSession stops the worker's agent and closes its pane once the packet is ready.
	EndSession func(m *ledger.Meta)
	// Close removes worktrees and archives (workstream.Close).
	Close func(m *ledger.Meta) error
	// Notify posts an inbox item / toast (kind: land, landed, unmerged, question).
	Notify func(m *ledger.Meta, kind, summary string)
	// Orchestrator sends a one-line message to the orchestrator (follow-ups to start).
	Orchestrator func(text string) error
	// GateTimeout bounds the ff gate; default 10 minutes.
	GateTimeout time.Duration
}

func (e *Env) notify(m *ledger.Meta, kind, sum string) {
	if e.Notify != nil {
		e.Notify(m, kind, sum)
	}
}

// Applies reports whether a workstream lands (instead of closing out).
func Applies(p *project.Project, m *ledger.Meta) bool {
	return m.Landing && p.Config.Landing.Enabled
}

// ErrHandedBack means Land found a problem only the worker can fix and sent it back.
var ErrHandedBack = errors.New("handed back to the worker")

func now() *time.Time { t := time.Now(); return &t }

func (e *Env) state(m *ledger.Meta) *ledger.LandState {
	if m.Land == nil {
		m.Land = &ledger.LandState{State: "working"}
	}
	return m.Land
}

// CheckReady is called when a landing worker reports done. If its packet is complete it
// marks the workstream ready, ends the worker's session and posts one "ready to land" item.
// It returns (false, reason) when the packet is missing or incomplete, so the caller can post
// an ordinary "done" with the reason.
func (e *Env) CheckReady(m *ledger.Meta) (bool, string) {
	if !Applies(e.P, m) || m.Phase != "" {
		return false, ""
	}
	pk, err := ReadPacket(e.L, m.Name)
	if err != nil {
		return false, "landing packet unreadable: " + err.Error()
	}
	if pk == nil {
		return false, "no landing packet (" + PacketPath(e.L, m.Name) + ")"
	}
	if pk.Status != "ready" {
		return false, "landing packet isn't marked status: ready"
	}
	for _, wt := range m.Worktrees {
		if files := uncommittedWork(wt.Path); len(files) > 0 {
			return false, fmt.Sprintf("uncommitted changes in %s: %s", wt.Repo, strings.Join(files, ", "))
		}
	}
	st := e.state(m)
	st.State, st.ReadyAt, st.Nudged, st.LastError = "ready", now(), false, ""
	m.Phase = "landing"
	if err := e.L.SaveMeta(m); err != nil {
		return false, err.Error()
	}
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "ready", Summary: pk.Title})
	if e.EndSession != nil {
		e.EndSession(m)
	}
	e.notify(m, "land", ReadySummary(e.P, m, pk))
	return true, ""
}

// ReadySummary is the one line shown with the Land decision.
func ReadySummary(p *project.Project, m *ledger.Meta, pk *Packet) string {
	var how []string
	for _, wt := range m.Worktrees {
		pol, ok := p.Config.LandingFor(wt.Repo)
		switch {
		case !ok:
			how = append(how, wt.Repo+": no landing policy")
		case pol.PR():
			how = append(how, fmt.Sprintf("%s: PR → %s", wt.Repo, pol.Base))
		default:
			how = append(how, fmt.Sprintf("%s: ff → %s", wt.Repo, pol.Base))
		}
	}
	s := "ready to land: " + pk.Title
	if len(how) > 0 {
		s += " (" + strings.Join(how, "; ") + ")"
	}
	if n := len(pk.Followups); n > 0 {
		s += fmt.Sprintf(", %d follow-up(s)", n)
	}
	return s
}

// Choice is what to do with one follow-up at Land: start (ask the orchestrator to create it
// now), queue (proposals queue) or drop.
type Choice struct {
	Action string // start | queue | drop
	Mode   string // interactive | autonomous (for start/queue; default: the worker's suggestion)
}

// Land lands a ready (or parked) workstream. choices are by follow-up index; missing ones
// default to queue. Problems the worker must fix go back to it (ErrHandedBack); problems the
// user must fix (dirty main checkout, missing token, network) are returned as errors and
// change nothing.
func (e *Env) Land(m *ledger.Meta, choices map[int]Choice) (string, error) {
	if !Applies(e.P, m) {
		return "", fmt.Errorf("%s doesn't use landing (close it out with `foreman ws finish`)", m.Name)
	}
	st := e.state(m)
	if st.State != "ready" && st.State != "parked" {
		return "", fmt.Errorf("%s isn't ready to land (state %s)", m.Name, st.State)
	}
	pk, err := ReadPacket(e.L, m.Name)
	if err != nil || pk == nil {
		return "", fmt.Errorf("%s: landing packet: %v", m.Name, err)
	}
	ctx := context.Background()

	// Pre-flight every repo before changing anything, so a failure doesn't half-land.
	type step struct {
		wt     ledger.Worktree
		pol    project.LandingRepo
		repo   string
		f      forge.Forge
		branch string // remote branch
	}
	var steps []step
	for _, wt := range m.Worktrees {
		pol, ok := e.P.Config.LandingFor(wt.Repo)
		if !ok {
			return "", fmt.Errorf("repo %s has no landing policy: run `foreman landing setup` (or close %s by hand)", wt.Repo, m.Name)
		}
		s := step{wt: wt, pol: pol, repo: filepath.Join(e.P.Root, wt.Repo)}
		if files := uncommittedWork(wt.Path); len(files) > 0 {
			return e.handback(m, fmt.Sprintf("Your worktree %s has uncommitted changes (%s). Commit them, or remove them if they're scratch.", wt.Path, strings.Join(files, ", ")))
		}
		if !pol.PR() {
			if d := dirty(s.repo); len(d) > 0 {
				return "", fmt.Errorf("can't fast-forward %s: the main checkout %s has uncommitted changes (%s)", pol.Base, s.repo, strings.Join(d, ", "))
			}
			if cur, _ := git(s.repo, "symbolic-ref", "--short", "HEAD"); cur != pol.Base {
				return "", fmt.Errorf("can't fast-forward %s: the main checkout %s is on %q", pol.Base, s.repo, cur)
			}
			if contains(st.Merged, wt.Repo) || gitOK(s.repo, "merge-base", "--is-ancestor", wt.Branch, pol.Base) {
				steps = append(steps, s) // already in base: nothing to do but record it
				continue
			}
			if !gitOK(s.repo, "merge-base", "--is-ancestor", pol.Base, wt.Branch) {
				return e.handback(m, fmt.Sprintf("%s has moved on since you branched, so %s can't be fast-forwarded. Rebase %s onto %s (resolve any conflicts), re-run the tests, commit.", pol.Base, wt.Branch, wt.Branch, pol.Base))
			}
			if pol.Gate != "" {
				to := e.GateTimeout
				if to == 0 {
					to = 10 * time.Minute
				}
				if err := runGate(wt.Path, pol.Gate, to); err != nil {
					return e.handback(m, "The landing gate failed in your worktree, so nothing was merged. Fix it and commit.\n"+err.Error())
				}
			}
			steps = append(steps, s)
			continue
		}
		// PR mode.
		f, err := NewForge(ctx, e.P, pol)
		if err != nil {
			return "", err
		}
		s.f = f
		if s.branch, err = RemoteBranch(pol.Branch, m); err != nil {
			return "", err
		}
		if _, err := git(s.repo, "fetch", "--quiet", pol.Upstream, pol.Base); err != nil {
			return "", fmt.Errorf("fetch %s %s in %s: %v", pol.Upstream, pol.Base, wt.Repo, err)
		}
		if bad, files, err := conflicts(s.repo, "FETCH_HEAD", wt.Branch); err != nil {
			return "", err
		} else if bad {
			return e.handback(m, fmt.Sprintf("%s conflicts with %s/%s (%s). foreman fetched it: rebase onto %s/%s, resolve the conflicts, re-run the tests, commit.", wt.Branch, pol.Upstream, pol.Base, files, pol.Upstream, pol.Base))
		}
		steps = append(steps, s)
	}

	// Act.
	var did []string
	for _, s := range steps {
		if !s.pol.PR() {
			if !contains(st.Merged, s.wt.Repo) {
				if !gitOK(s.repo, "merge-base", "--is-ancestor", s.wt.Branch, s.pol.Base) {
					if _, err := git(s.repo, "merge", "--ff-only", "--quiet", s.wt.Branch); err != nil {
						return e.fail(m, did, fmt.Errorf("fast-forward %s in %s: %v", s.pol.Base, s.wt.Repo, err))
					}
				}
				st.Merged = append(st.Merged, s.wt.Repo)
				_ = e.L.SaveMeta(m)
			}
			did = append(did, fmt.Sprintf("%s: fast-forwarded %s", s.wt.Repo, s.pol.Base))
			continue
		}
		if err := push(s.repo, s.pol.Remote, s.wt.Branch, s.branch, prFor(st, s.wt.Repo) != nil); err != nil {
			return e.fail(m, did, err)
		}
		sha, _ := git(s.repo, "rev-parse", s.wt.Branch)
		mr, err := s.f.Find(ctx, s.branch, s.pol.HeadOwner)
		if err != nil {
			return e.fail(m, did, fmt.Errorf("look up PR for %s: %w", s.branch, err))
		}
		verb := "updated"
		if mr == nil || mr.State != forge.Open {
			mr, err = s.f.Open(ctx, forge.OpenReq{Head: s.branch, HeadOwner: s.pol.HeadOwner, Base: s.pol.Base,
				Title: pk.Title, Body: prBody(pk, m), Draft: s.pol.Draft})
			if err != nil {
				return e.fail(m, did, fmt.Errorf("open PR for %s: %w", s.branch, err))
			}
			verb = "opened"
		}
		setPR(st, ledger.PRRef{Repo: s.wt.Repo, Branch: s.branch, ID: mr.ID, URL: mr.URL, State: mr.State, SHA: sha})
		_ = e.L.SaveMeta(m)
		did = append(did, fmt.Sprintf("%s: %s %s", s.wt.Repo, verb, mr.URL))
	}

	if !st.FollowupsFiled {
		did = append(did, e.fileFollowups(m, pk, choices)...)
		st.FollowupsFiled = true
	}
	st.LastError = ""
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "land", Summary: strings.Join(did, "; ")})
	if len(st.PRs) > 0 {
		st.State = "in-review"
		if err := e.L.SaveMeta(m); err != nil {
			return "", err
		}
		e.ackItems(m)
		return strings.Join(did, "; "), nil
	}
	return strings.Join(append(did, e.finalize(m, pk)...), "; "), nil
}

func (e *Env) fail(m *ledger.Meta, did []string, err error) (string, error) {
	m.Land.LastError = err.Error()
	_ = e.L.SaveMeta(m)
	if len(did) > 0 {
		err = fmt.Errorf("%w (already done: %s; pressing Land again continues from here)", err, strings.Join(did, "; "))
	}
	return "", err
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func prFor(st *ledger.LandState, repo string) *ledger.PRRef {
	for i := range st.PRs {
		if st.PRs[i].Repo == repo {
			return &st.PRs[i]
		}
	}
	return nil
}

func setPR(st *ledger.LandState, r ledger.PRRef) {
	if p := prFor(st, r.Repo); p != nil {
		*p = r
		return
	}
	st.PRs = append(st.PRs, r)
}

// push pushes the local branch to the remote branch. A rejected non-fast-forward update
// (the worker rebased during review) is retried with --force-with-lease pinned to the remote
// sha just read, and only for a branch foreman pushed before.
func push(repo, remote, local, remoteBranch string, ours bool) error {
	ref := "refs/heads/" + remoteBranch
	_, err := git(repo, "push", "--quiet", remote, local+":"+ref)
	if err == nil {
		return nil
	}
	if !ours || !(strings.Contains(err.Error(), "non-fast-forward") || strings.Contains(err.Error(), "rejected")) {
		return fmt.Errorf("push %s to %s: %v", local, remote, err)
	}
	out, lerr := git(repo, "ls-remote", remote, ref)
	if lerr != nil || out == "" {
		return fmt.Errorf("push %s to %s: %v", local, remote, err)
	}
	sha := strings.Fields(out)[0]
	if _, err := git(repo, "push", "--quiet", "--force-with-lease="+ref+":"+sha, remote, local+":"+ref); err != nil {
		return fmt.Errorf("push %s to %s (with lease): %v", local, remote, err)
	}
	return nil
}

var branchVar = regexp.MustCompile(`\{[a-z]+\}`)

// RemoteBranch renders the remote branch template ({name}, {ticket}) for a workstream.
func RemoteBranch(tmpl string, m *ledger.Meta) (string, error) {
	if tmpl == "" {
		tmpl = "fm/{name}"
	}
	var missing string
	out := branchVar.ReplaceAllStringFunc(tmpl, func(v string) string {
		switch v {
		case "{name}":
			return m.Name
		case "{ticket}":
			if m.Ticket == "" {
				missing = "ticket"
			}
			return m.Ticket
		}
		missing = strings.Trim(v, "{}")
		return ""
	})
	if missing != "" {
		return "", fmt.Errorf("branch template %q needs {%s}, which %s doesn't have", tmpl, missing, m.Name)
	}
	if !gitOK(".", "check-ref-format", "--branch", out) {
		return "", fmt.Errorf("branch template %q gives an invalid branch name %q", tmpl, out)
	}
	return out, nil
}

func prBody(pk *Packet, m *ledger.Meta) string {
	var b strings.Builder
	b.WriteString(pk.Description)
	if pk.Tests != "" {
		b.WriteString("\n\n### Tests\n" + pk.Tests)
	}
	return strings.TrimSpace(b.String())
}

// NewForge builds the forge client for a repo policy, resolving its token from config.
func NewForge(ctx context.Context, p *project.Project, pol project.LandingRepo) (forge.Forge, error) {
	fc, ok := p.Config.Forges[pol.Forge]
	if !ok {
		return nil, fmt.Errorf("no [forges.%q] entry: run `foreman landing setup`", pol.Forge)
	}
	tok, err := forge.Token(ctx, fc.TokenCmd, fc.TokenEnv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pol.Forge, err)
	}
	api := fc.API
	if api == "" {
		api = forge.DefaultAPI(fc.Kind, pol.Forge)
	}
	return forge.New(forge.Config{Kind: fc.Kind, API: api, Project: pol.Project, Token: tok})
}

// handback restarts a worker with what it needs to fix; the workstream goes back to working.
func (e *Env) handback(m *ledger.Meta, problem string) (string, error) {
	st := e.state(m)
	st.State, st.LastError = "working", problem
	m.Phase = ""
	if err := e.L.SaveMeta(m); err != nil {
		return "", err
	}
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "handback", Summary: problem})
	e.ackItems(m)
	msg := fmt.Sprintf("[foreman → %s] Landing stopped: %s\nWhen it's fixed and committed, update %s (keep `status: ready`), set your report to `status: done`, and stop. foreman will ask the user to land again.",
		m.Name, problem, PacketPath(e.L, m.Name))
	if e.Handback == nil {
		return "", fmt.Errorf("%w: %s", ErrHandedBack, problem)
	}
	if err := e.Handback(m, msg); err != nil {
		return "", fmt.Errorf("%s needs its worker (%s), but restarting it failed: %v", m.Name, problem, err)
	}
	return "", fmt.Errorf("%w: %s", ErrHandedBack, problem)
}

func (e *Env) ackItems(m *ledger.Meta) {
	for _, it := range e.L.Pending() {
		if it.WS == m.Name {
			_ = e.L.Ack(it.ID)
		}
	}
}

// fileFollowups files every drafted follow-up in the proposals queue with the user's choice.
// "start" also asks the orchestrator to create the workstream now.
func (e *Env) fileFollowups(m *ledger.Meta, pk *Packet, choices map[int]Choice) []string {
	var started, queued, dropped int
	for i, f := range pk.Followups {
		c, ok := choices[i]
		if !ok || c.Action == "" {
			c.Action = "queue"
		}
		mode := c.Mode
		if mode == "" {
			mode = f.Mode
		}
		status := map[string]string{"start": "start", "queue": "queued", "drop": "dropped"}[c.Action]
		if status == "" {
			status = "queued"
		}
		id := fmt.Sprintf("%s-%d-%s", m.Name, i+1, time.Now().Format("0102150405"))
		_ = e.L.AddProposal(ledger.Proposal{ID: id, From: m.Name, Title: f.Title, Goal: f.Goal, Type: f.Type, Mode: mode, Status: status})
		switch status {
		case "start":
			started++
			if e.Orchestrator != nil {
				_ = e.Orchestrator(StartMessage(ledger.Proposal{ID: id, From: m.Name, Title: f.Title, Goal: f.Goal, Type: f.Type, Mode: mode}))
			}
		case "queued":
			queued++
		default:
			dropped++
		}
	}
	if len(pk.Followups) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("follow-ups: %d to start, %d queued, %d dropped", started, queued, dropped)}
}

// StartMessage asks the orchestrator to turn an approved proposal into a workstream.
func StartMessage(pr ledger.Proposal) string {
	t := pr.Type
	if t == "" {
		t = "(pick one)"
	}
	return fmt.Sprintf("[foreman → orchestrator] The user approved follow-up %s from %s: %q (type %s, mode %s). Goal: %s\nWrite its brief and create it with `foreman ws new` now; then mark it with `foreman proposals started %s`.",
		pr.ID, pr.From, pr.Title, t, pr.Mode, pr.Goal, pr.ID)
}

// Poll checks the open PRs of an in-review workstream. When all are merged it finalizes;
// when one was closed without merging it asks the user (park, close or keep working).
func (e *Env) Poll(m *ledger.Meta) error {
	st := m.Land
	if st == nil || st.State != "in-review" {
		return nil
	}
	ctx := context.Background()
	changed := false
	for i := range st.PRs {
		pr := &st.PRs[i]
		if pr.State != forge.Open {
			continue
		}
		pol, ok := e.P.Config.LandingFor(pr.Repo)
		if !ok {
			continue
		}
		f, err := NewForge(ctx, e.P, pol)
		if err != nil {
			return err
		}
		mr, err := f.Find(ctx, pr.Branch, pol.HeadOwner)
		if err != nil {
			return err
		}
		if mr == nil || mr.ID != pr.ID {
			continue
		}
		if mr.State != pr.State {
			pr.State, changed = mr.State, true
		}
	}
	if !changed {
		return nil
	}
	allMerged, anyClosed := true, false
	for _, pr := range st.PRs {
		allMerged = allMerged && pr.State == forge.Merged
		anyClosed = anyClosed || pr.State == forge.Closed
	}
	switch {
	case allMerged:
		pk, _ := ReadPacket(e.L, m.Name)
		e.finalize(m, pk)
	case anyClosed:
		st.State = "unmerged"
		_ = e.L.SaveMeta(m)
		_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "unmerged"})
		e.notify(m, "unmerged", "PR closed without merging: park it (p), keep working (k) or close it (c)")
	default:
		_ = e.L.SaveMeta(m)
	}
	return nil
}

// finalize runs once the work is in: apply the foreman-docs patch, record landed, close.
func (e *Env) finalize(m *ledger.Meta, pk *Packet) []string {
	var did []string
	st := e.state(m)
	if pk != nil && len(pk.DocsPatch) > 0 {
		switch e.P.Config.Docs.Mode {
		case "local", "external":
			if err := applyDocsPatch(e.P.DocsDir(), pk.DocsPatch); err != nil {
				e.notify(m, "question", "landed, but "+err.Error()+"; start a short docs workstream to redo it")
				did = append(did, "docs patch NOT applied: "+err.Error())
			} else {
				did = append(did, "docs patch applied to "+e.P.DocsDir())
			}
		default:
			did = append(did, "docs patch ignored (docs mode commit: doc edits belong on the branch)")
		}
	}
	st.State, st.LandedAt = "landed", now()
	_ = e.L.SaveMeta(m)
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "landed", Summary: strings.Join(did, "; ")})
	e.ackItems(m)
	if e.Close != nil {
		if err := e.Close(m); err != nil {
			e.notify(m, "question", "landed, but closing failed: "+err.Error())
			return append(did, "close failed: "+err.Error())
		}
		did = append(did, "closed and archived")
	}
	e.notify(m, "landed", "landed: "+strings.Join(did, "; "))
	return did
}

// Park keeps a ready or unmerged workstream open and idle: branch and PR untouched, no nudges.
func (e *Env) Park(m *ledger.Meta) error {
	st := e.state(m)
	switch st.State {
	case "ready", "unmerged", "in-review":
	default:
		return fmt.Errorf("%s can't be parked from state %s", m.Name, st.State)
	}
	st.State = "parked"
	m.Phase = "landing"
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "parked"})
	e.ackItems(m)
	return e.L.SaveMeta(m)
}

// Keep sends the workstream back to a worker on the same branch with the user's note (review
// comments, a fix). The packet stays; the worker updates it and reports done again. An open
// PR is updated in place at the next Land.
func (e *Env) Keep(m *ledger.Meta, note string) error {
	if !Applies(e.P, m) {
		return fmt.Errorf("%s doesn't use landing", m.Name)
	}
	if strings.TrimSpace(note) == "" {
		return errors.New("say what to change")
	}
	st := e.state(m)
	if st.State == "landed" {
		return fmt.Errorf("%s has already landed: start a follow-up instead", m.Name)
	}
	st.State, st.LastError = "working", ""
	m.Phase = ""
	if err := e.L.SaveMeta(m); err != nil {
		return err
	}
	_ = e.L.AddEvent(ledger.Event{WS: m.Name, Kind: "keep-working", Summary: note})
	e.ackItems(m)
	msg := fmt.Sprintf("[foreman → %s] The user wants more work before landing: %s\nWork on the same branch and add commits (don't rebase or force-push unless asked: reviewers may already be looking). Then update %s (keep `status: ready`), set your report to `status: done`, and stop.",
		m.Name, note, PacketPath(e.L, m.Name))
	if e.Handback == nil {
		return errors.New("no way to restart the worker")
	}
	return e.Handback(m, msg)
}

// Due reports whether a ready workstream has waited long enough for its one nudge.
func Due(p *project.Project, m *ledger.Meta) bool {
	st := m.Land
	h := p.Config.Landing.NudgeHours
	return st != nil && st.State == "ready" && !st.Nudged && h > 0 && st.ReadyAt != nil && time.Since(*st.ReadyAt) > time.Duration(h)*time.Hour
}

// PollDue reports whether an in-review workstream should be polled now.
func PollDue(p *project.Project, last time.Time) bool {
	mins := p.Config.Landing.PollMinutes
	if mins <= 0 {
		mins = 10
	}
	return time.Since(last) >= time.Duration(mins)*time.Minute
}
