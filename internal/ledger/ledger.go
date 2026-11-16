// Package ledger is the project's local record of workstreams, events and the inbox.
//
// Races are avoided by ownership, not locks: meta.json is written only by CLI commands (and
// the watcher's closeout finalizer, once the worker is done), status.json only by the watcher,
// report.md and closeout.md only by the worker. events.jsonl, inbox.jsonl and acks.jsonl are
// append-only. Closed workstreams move from ws/ to archive/.
package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PVjammer/foreman/internal/project"
)

type Worktree struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// Meta is the workstream's identity and setup. Owner: CLI.
type Meta struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"` // the workstream type: feature, bug, research, ...
	Profile     string     `json:"profile,omitempty"`
	Harness     string     `json:"harness"`
	Args        []string   `json:"args,omitempty"`
	Goal        string     `json:"goal"`
	Ticket      string     `json:"ticket,omitempty"`
	Repos       []string   `json:"repos,omitempty"`
	Cwd         string     `json:"cwd"`
	Worktrees   []Worktree `json:"worktrees,omitempty"`
	WorkspaceID string     `json:"workspace_id"`
	TabID       string     `json:"tab_id,omitempty"`
	PaneID      string     `json:"pane_id"`
	// Phase is "" while working, "closeout" once asked to draft its closeout, "applying"
	// after the user approved it, and "landing" from ready-to-land on (see Land).
	Phase    string     `json:"phase,omitempty"`
	Closed   bool       `json:"closed"`
	Created  time.Time  `json:"created"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// Landing is set at creation when the project has landing enabled (and --no-landing
	// wasn't given). Without it the workstream closes out as before.
	Landing bool       `json:"landing,omitempty"`
	Land    *LandState `json:"land,omitempty"`
}

// LandState tracks a landing workstream from ready to landed. Owner: CLI and the watcher's
// landing steps (like the closeout finalizer).
type LandState struct {
	// State: working | ready | in-review | parked | unmerged | landed
	State          string     `json:"state"`
	ReadyAt        *time.Time `json:"ready_at,omitempty"`
	LandedAt       *time.Time `json:"landed_at,omitempty"`
	Nudged         bool       `json:"nudged,omitempty"`
	FollowupsFiled bool       `json:"followups_filed,omitempty"`
	PRs            []PRRef    `json:"prs,omitempty"`
	Merged         []string   `json:"merged,omitempty"` // repos fast-forwarded locally
	LastError      string     `json:"last_error,omitempty"`
}

// PRRef is a pull/merge request foreman opened for one repo of a workstream.
type PRRef struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"` // remote branch name
	ID     string `json:"id"`
	URL    string `json:"url"`
	State  string `json:"state"` // open | merged | closed
	SHA    string `json:"sha,omitempty"`
}

// Proposal is a follow-up drafted by a worker and filed by foreman. proposals.jsonl is
// append-only; a later line with the same ID updates its status.
type Proposal struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	From   string    `json:"from"` // source workstream
	Title  string    `json:"title"`
	Goal   string    `json:"goal,omitempty"`
	Type   string    `json:"type,omitempty"`
	Mode   string    `json:"mode,omitempty"` // interactive | autonomous (suggested; chosen at start)
	Status string    `json:"status"`         // queued | start | dropped | started
	Note   string    `json:"note,omitempty"`
}

// Status is the live state as last observed. Owner: watcher.
type Status struct {
	AgentStatus   string    `json:"agent_status"`
	ReportStatus  string    `json:"report_status,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	LastEvent     string    `json:"last_event,omitempty"`
	Updated       time.Time `json:"updated"`
	StateSeq      uint64    `json:"state_seq"`
	CompletionSeq uint64    `json:"completion_seq"`
	Turns         int       `json:"turns,omitempty"`
	HygieneAt     time.Time `json:"hygiene_at,omitempty"`
	CloseoutMod   time.Time `json:"closeout_mod,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	WS      string    `json:"ws"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary,omitempty"`
}

// Item kinds: blocked | question | closeout | done | stale | exited | turn, and for landing:
// land (ready to land) | unmerged (PR closed without merging) | landed
type Item struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	WS      string    `json:"ws"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}

type Ledger struct{ P *project.Project }

func New(p *project.Project) *Ledger { return &Ledger{P: p} }

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func readLines[T any](path string) []T {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (l *Ledger) SaveMeta(m *Meta) error {
	dir := l.P.WorkstreamDir(m.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "meta.json"), m)
}

func (l *Ledger) Meta(name string) (*Meta, error) {
	b, err := os.ReadFile(filepath.Join(l.P.WorkstreamDir(name), "meta.json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	return &m, json.Unmarshal(b, &m)
}

func (l *Ledger) SaveStatus(name string, s *Status) error {
	return writeJSON(filepath.Join(l.P.WorkstreamDir(name), "status.json"), s)
}

func (l *Ledger) Status(name string) *Status {
	var s Status
	if b, err := os.ReadFile(filepath.Join(l.P.WorkstreamDir(name), "status.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return &s
}

// List returns workstreams, newest first; includeClosed also reads the archive.
func (l *Ledger) List(includeClosed bool) []*Meta {
	dirs := []string{"ws"}
	if includeClosed {
		dirs = append(dirs, "archive")
	}
	var out []*Meta
	seen := map[string]bool{}
	for _, d := range dirs {
		entries, _ := os.ReadDir(l.P.Path(d))
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			m, err := l.Meta(e.Name())
			if err != nil || (m.Closed && !includeClosed) {
				continue
			}
			seen[e.Name()] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func (l *Ledger) BriefPath(name string) string {
	return filepath.Join(l.P.WorkstreamDir(name), "brief.md")
}
func (l *Ledger) ReportPath(name string) string {
	return filepath.Join(l.P.WorkstreamDir(name), "report.md")
}
func (l *Ledger) CloseoutPath(name string) string {
	return filepath.Join(l.P.WorkstreamDir(name), "closeout.md")
}

// Archive moves a closed workstream's folder from ws/ to archive/.
func (l *Ledger) Archive(name string) error {
	src := l.P.Path("ws", name)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if err := os.MkdirAll(l.P.Path("archive"), 0o755); err != nil {
		return err
	}
	dst := l.P.Path("archive", name)
	if _, err := os.Stat(dst); err == nil {
		dst += "-" + time.Now().Format("20060102-150405")
	}
	return os.Rename(src, dst)
}

// Report returns the worker's report and its frontmatter status (done, needs-input, ...).
func (l *Ledger) Report(name string) (body, status string, mod time.Time) {
	return readFrontmatter(l.ReportPath(name))
}

// Closeout returns the worker's closeout draft and its status (proposed, applied).
func (l *Ledger) Closeout(name string) (body, status string, mod time.Time) {
	return readFrontmatter(l.CloseoutPath(name))
}

func readFrontmatter(path string) (body, status string, mod time.Time) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", time.Time{}
	}
	b, _ := os.ReadFile(path)
	body = string(b)
	if strings.HasPrefix(body, "---") {
		if end := strings.Index(body[3:], "---"); end > 0 {
			for _, line := range strings.Split(body[3:3+end], "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "status" {
					status = strings.TrimSpace(v)
				}
			}
		}
	}
	return body, status, fi.ModTime()
}

// Section pulls one "## Heading" section out of a markdown report.
func Section(md, heading string) string {
	lines := strings.Split(md, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			if in {
				break
			}
			in = strings.EqualFold(strings.TrimSpace(ln[3:]), heading)
			continue
		}
		if in {
			out = append(out, ln)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Kickoff is a first prompt waiting for its agent to become ready. Written by the CLI,
// consumed (deleted) by the watcher.
func (l *Ledger) kickoffPath(name string) string {
	return filepath.Join(l.P.WorkstreamDir(name), "kickoff.txt")
}

func (l *Ledger) SetKickoff(name, prompt string) error {
	return os.WriteFile(l.kickoffPath(name), []byte(prompt), 0o644)
}

func (l *Ledger) Kickoff(name string) string {
	b, _ := os.ReadFile(l.kickoffPath(name))
	return string(b)
}

func (l *Ledger) ClearKickoff(name string) { _ = os.Remove(l.kickoffPath(name)) }

func (l *Ledger) AddEvent(e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	return appendJSON(l.P.Path("events.jsonl"), e)
}

func (l *Ledger) Events(since time.Time) []Event {
	var out []Event
	for _, e := range readLines[Event](l.P.Path("events.jsonl")) {
		if !e.Time.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

func (l *Ledger) AddItem(it Item) error {
	if it.Time.IsZero() {
		it.Time = time.Now()
	}
	if it.ID == "" {
		it.ID = it.Time.Format("20060102T150405.000") + "-" + it.WS
	}
	return appendJSON(l.P.Path("inbox.jsonl"), it)
}

func (l *Ledger) Ack(id string) error {
	return appendJSON(l.P.Path("acks.jsonl"), map[string]any{"id": id, "time": time.Now()})
}

// Pending returns unacknowledged inbox items, keeping only the newest per workstream:
// a later event supersedes an earlier one (a "done" replaces a stale "blocked").
func (l *Ledger) Pending() []Item {
	acked := map[string]bool{}
	for _, a := range readLines[struct{ ID string }](l.P.Path("acks.jsonl")) {
		acked[a.ID] = true
	}
	latest := map[string]Item{}
	for _, it := range readLines[Item](l.P.Path("inbox.jsonl")) {
		latest[it.WS] = it
	}
	var out []Item
	for _, it := range latest {
		if !acked[it.ID] {
			out = append(out, it)
		}
	}
	// blocked and questions first, then newest
	rank := map[string]int{"blocked": 0, "question": 1, "land": 2, "closeout": 2, "unmerged": 2, "exited": 3, "done": 4, "landed": 4, "stale": 5, "turn": 6}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Kind] != rank[out[j].Kind] {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Time.After(out[j].Time)
	})
	return out
}

func (l *Ledger) AddBacklog(text, ws string) error {
	f, err := os.OpenFile(l.P.Path("backlog.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	src := ""
	if ws != "" {
		src = " (from " + ws + ")"
	}
	_, err = f.WriteString("- [ ] " + time.Now().Format("2006-01-02") + " " + text + src + "\n")
	return err
}

func (l *Ledger) AddProposal(pr Proposal) error {
	if pr.Time.IsZero() {
		pr.Time = time.Now()
	}
	return appendJSON(l.P.Path("proposals.jsonl"), pr)
}

// Proposals returns the latest version of each proposal, oldest first.
func (l *Ledger) Proposals() []Proposal {
	latest := map[string]Proposal{}
	var order []string
	for _, pr := range readLines[Proposal](l.P.Path("proposals.jsonl")) {
		if _, ok := latest[pr.ID]; !ok {
			order = append(order, pr.ID)
		}
		latest[pr.ID] = pr
	}
	out := make([]Proposal, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out
}
