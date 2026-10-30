// Package watch is the per-project background loop that turns herdr agent state changes into
// ledger updates, sidebar summaries and — depending on the UI mode — toasts, inbox items, or
// messages relayed into the orchestrator conversation. It also drives closeout (spotting the
// worker's draft and finishing the workstream once it is applied) and nudges about stale or
// bloated workstreams.
//
// One watcher runs per open project, as a detached process holding .foreman/logs/watch.lock.
// It exits when the project's herdr workspace closes or the herdr server goes away.
//
// foreman's herdr plugin wakes it on every agent state change (`foreman hook event` sends a
// datagram to its socket), so it reacts at once. It still polls as a fallback: every 1.5s
// until the first wake arrives, then every 6s.
package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/summarize"
	"github.com/PVjammer/foreman/internal/views"
	"github.com/PVjammer/foreman/internal/workstream"
)

type seen struct {
	present   bool
	status    string
	stateSeq  uint64
	compSeq   uint64
	reportMod time.Time
}

type sent struct {
	at       time.Time
	stateSeq uint64
}

type Watcher struct {
	P   *project.Project
	L   *ledger.Ledger
	H   *herdr.Client
	Log *log.Logger

	spaceID    string
	orchPane   string
	seen       map[string]seen
	mu         sync.Mutex
	relay      []string
	inboxN     int
	parked     map[string]string // pane → prompt to send once its agent is ready
	verify     map[string]sent   // pane → held prompt just delivered, to confirm it was submitted
	lastHyg    time.Time
	warnedOpen bool
}

func New(p *project.Project, l *log.Logger) *Watcher {
	return &Watcher{P: p, L: ledger.New(p), H: space.Client(p), Log: l, seen: map[string]seen{}, inboxN: -1, parked: map[string]string{}, verify: map[string]sent{}}
}

// Lock takes the project's watcher lock. ok=false means another watcher holds it.
func Lock(p *project.Project) (release func(), ok bool, err error) {
	_ = os.MkdirAll(p.Path("logs"), 0o755)
	f, err := os.OpenFile(p.Path("logs", "watch.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false, nil
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true, nil
}

// SocketPath is where a project's watcher listens for wake-ups. It lives under the data dir,
// not the project, because unix socket paths are limited to about 100 bytes.
func SocketPath(p *project.Project) string {
	return filepath.Join(project.DataDir(), "run", project.Slug(p.Config.Name)+".sock")
}

// Wake nudges the project's watcher to look at herdr now. It fails if no watcher listens.
func Wake(p *project.Project) error {
	c, err := net.Dial("unixgram", SocketPath(p))
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte("wake"))
	return err
}

func (w *Watcher) listen() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	path := SocketPath(w.P)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.Remove(path) // stale socket from a watcher that died; we hold the lock now
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		w.Log.Printf("wake socket: %v (polling only)", err)
		return ch, func() {}
	}
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := conn.ReadFrom(buf); err != nil {
				return
			}
			select { // coalesce bursts: one pending wake is enough
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch, func() { conn.Close(); os.Remove(path) }
}

// Running reports whether a watcher holds the project's lock.
func Running(p *project.Project) bool {
	release, ok, err := Lock(p)
	if err != nil {
		return false
	}
	if ok {
		release()
		return false
	}
	return true
}

// Spawn starts a detached `foreman watch --daemon` for the project unless one is running.
func Spawn(p *project.Project) error {
	if Running(p) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(p.Path("logs", "watch.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "watch", "--daemon", "--bootstrap")
	cmd.Dir = p.Root
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Run blocks until ctx is cancelled, the project's workspace closes, or the server is gone.
// bootstrap=true starts or rehydrates the orchestrator and restarts workstreams that herdr
// did not resume natively — once per herdr server lifetime.
func (w *Watcher) Run(ctx context.Context, bootstrap bool) {
	if !w.findSpace() {
		w.Log.Printf("project %q has no herdr workspace in session %s; nothing to watch", w.P.Config.Name, w.P.Config.Herdr.Session)
		return
	}
	wake, closeSock := w.listen()
	defer closeSock()
	if bootstrap {
		w.bootstrap(ctx)
	}
	w.tick(true)
	interval := 1500 * time.Millisecond
	t := time.NewTimer(interval)
	defer t.Stop()
	misses, failures := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if interval < 6*time.Second {
				w.Log.Printf("woken by the herdr plugin; polling every 6s from now on")
				interval = 6 * time.Second
			}
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		case <-t.C:
		}
		t.Reset(interval)
		w.reload()
		// The workspace can be closed by `foreman down` or by hand.
		if _, err := w.H.Workspaces(); err != nil {
			failures++
		} else if !w.findSpace() {
			misses++
		} else {
			misses, failures = 0, 0
		}
		if misses >= 1 { // the list call succeeded and our workspace isn't in it
			w.Log.Printf("workspace closed; watcher exiting")
			return
		}
		if failures >= 4 {
			w.Log.Printf("herdr server unreachable; watcher exiting")
			return
		}
		w.tick(false)
		w.flushRelay()
		w.closeouts()
		w.hygiene()
		w.updateInboxCount()
	}
}

func (w *Watcher) findSpace() bool {
	sp, err := space.Find(w.P, w.H)
	if err != nil || sp.ID == "" {
		return false
	}
	w.spaceID = sp.ID
	if pane, err := sp.OrchestratorPane(); err == nil {
		w.orchPane = pane
	}
	return true
}

// reload re-reads config each tick so `foreman mode` switches candidates live.
func (w *Watcher) reload() {
	if p, err := project.Load(w.P.Root); err == nil {
		w.P.Config = p.Config
	}
}

func (w *Watcher) tick(initial bool) {
	agents, err := w.H.Agents()
	if err != nil {
		w.Log.Printf("agent list: %v", err)
		return
	}
	byPane, byName := map[string]herdr.Agent{}, map[string]herdr.Agent{}
	for _, a := range agents {
		byPane[a.PaneID] = a
		if a.Name != "" {
			byName[a.Name] = a
		}
	}
	for pane, text := range w.parked {
		if a, ok := byPane[pane]; ok && (a.Status == "idle" || a.Status == "done") {
			if err := w.H.Prompt(pane, text); err == nil {
				delete(w.parked, pane)
				w.verify[pane] = sent{time.Now(), a.StateChangeSeq}
				w.Log.Printf("delivered held prompt to %s", pane)
			}
		}
	}
	// The outbox: prompts CLI commands queued for agents that weren't ready (a workstream
	// request while the orchestrator was busy, scratch's first prompt).
	for _, q := range views.Outbox(w.P) {
		if a, ok := byPane[q.Pane]; ok && (a.Status == "idle" || a.Status == "done") {
			if err := w.H.Prompt(q.Pane, q.Text); err == nil {
				_ = os.Remove(q.Path)
				w.verify[q.Pane] = sent{time.Now(), a.StateChangeSeq}
				w.Log.Printf("delivered queued prompt to %s", q.Pane)
			}
		}
	}
	// A harness that is still loading can take the text but drop the Enter, leaving the prompt
	// in its input box. If nothing happened a few seconds after delivery, press Enter once.
	for pane, v := range w.verify {
		a, ok := byPane[pane]
		switch {
		case !ok || a.StateChangeSeq != v.stateSeq || a.Status == "working":
			delete(w.verify, pane)
		case time.Since(v.at) > 5*time.Second && (a.Status == "idle" || a.Status == "done"):
			_ = w.H.SendKeys(pane, "enter")
			w.Log.Printf("%s: held prompt wasn't submitted; pressed enter", pane)
			delete(w.verify, pane)
		}
	}
	for _, m := range w.L.List(false) {
		want := space.AgentName(w.P, m.Name)
		a, ok := byPane[m.PaneID]
		if ok && a.Name != "" && a.Name != want {
			ok = false // the pane now holds someone else's agent: its events aren't ours
		}
		if !ok {
			a, ok = byName[want]
		}
		prev, known := w.seen[m.Name]
		if !ok {
			// A worker vanishing because its whole space closed (`foreman down`, quitting
			// herdr) is not news; only report exits from a space that is still open.
			if known && prev.present && !initial && w.findSpace() {
				go w.emit(m, "exited", false)
			}
			w.seen[m.Name] = seen{present: false, reportMod: prev.reportMod}
			continue
		}
		if (a.Status == "idle" || a.Status == "done") && w.L.Kickoff(m.Name) != "" {
			if err := w.H.Prompt(a.PaneID, w.L.Kickoff(m.Name)); err == nil {
				w.L.ClearKickoff(m.Name)
				w.Log.Printf("%s: delivered parked kickoff", m.Name)
			}
		}
		cur := seen{present: true, status: a.Status, stateSeq: a.StateChangeSeq, compSeq: a.CompletionSeq, reportMod: prev.reportMod}
		if initial || !known {
			_, _, cur.reportMod = w.L.Report(m.Name)
			w.seen[m.Name] = cur
			// Record the first observation too: bootstrap reads this after the next restart.
			st := w.L.Status(m.Name)
			st.AgentStatus, st.StateSeq, st.CompletionSeq, st.Updated = a.Status, a.StateChangeSeq, a.CompletionSeq, time.Now()
			_ = w.L.SaveStatus(m.Name, st)
			continue
		}
		if cur.status == prev.status && cur.compSeq == prev.compSeq {
			continue
		}
		kind := ""
		switch {
		case cur.status == "blocked" && prev.status != "blocked":
			kind = "blocked"
		case cur.compSeq > prev.compSeq || (prev.status == "working" && (cur.status == "idle" || cur.status == "done")):
			kind = "turn"
			_, rs, mod := w.L.Report(m.Name)
			if mod.After(prev.reportMod) {
				cur.reportMod = mod
				switch rs {
				case "needs-input", "blocked":
					kind = "question"
				case "done", "failed":
					kind = "done"
				}
			}
		}
		// During closeout the closeout draft, not the report, is what matters.
		if m.Phase != "" && (kind == "turn" || kind == "done") {
			kind = ""
		}
		w.seen[m.Name] = cur
		st := w.L.Status(m.Name)
		st.AgentStatus, st.StateSeq, st.CompletionSeq, st.Updated = a.Status, a.StateChangeSeq, a.CompletionSeq, time.Now()
		if cur.compSeq > prev.compSeq {
			st.Turns++
		}
		_ = w.L.SaveStatus(m.Name, st)
		if kind != "" {
			go w.emit(m, kind, a.Focused)
		}
	}
}

var labels = map[string]string{
	"blocked":  "⛔ blocked",
	"question": "❓ needs you",
	"closeout": "📝 closeout ready",
	"done":     "✅ done",
	"stale":    "🧹 close out?",
	"turn":     "· turn ended",
	"exited":   "✖ exited",
}

func (w *Watcher) emit(m *ledger.Meta, kind string, focused bool) {
	report, rstatus, _ := w.L.Report(m.Name)
	screen := ""
	if kind != "exited" {
		screen, _ = w.H.Read(m.PaneID, 40)
	}
	s := &summarize.Summarizer{URL: w.P.Config.Summarizer.URL, Model: w.P.Config.Summarizer.Model, Enabled: w.P.Config.SummarizerEnabled()}
	sum, err := s.Summarize(m.Name, kind, reportFor(kind, report), screen)
	if err != nil || sum == "" {
		if err != nil {
			w.Log.Printf("summarize %s: %v", m.Name, err)
		}
		sum = fallbackSummary(kind, report)
	}
	w.post(m, kind, sum, rstatus, focused)
}

// post records an event and delivers it according to the UI mode.
func (w *Watcher) post(m *ledger.Meta, kind, sum, rstatus string, focused bool) {
	if m.PaneID != "" && kind != "exited" {
		_ = w.H.PaneToken(m.PaneID, "summary", sum)
	}
	st := w.L.Status(m.Name)
	st.Summary, st.LastEvent, st.Updated = sum, kind, time.Now()
	if rstatus != "" {
		st.ReportStatus = rstatus
	}
	_ = w.L.SaveStatus(m.Name, st)
	_ = w.L.AddEvent(ledger.Event{WS: m.Name, Kind: kind, Summary: sum})
	w.Log.Printf("%s %s: %s", m.Name, kind, sum)

	// A plain "turn ended" with no report is low-signal: sidebar only.
	if kind == "turn" || focused {
		return
	}
	title := w.P.Config.Name + " · " + m.Name + "  " + labels[kind]
	sound := "done"
	if kind == "blocked" || kind == "question" || kind == "closeout" {
		sound = "request"
	}
	switch w.P.Config.UI.Mode {
	case "notifier":
		_ = w.H.Notify(title, sum, sound)
	case "relay":
		w.mu.Lock()
		w.relay = append(w.relay, fmt.Sprintf("[foreman → orchestrator] %s %s: %s (report: %s)", m.Name, kind, sum, w.L.ReportPath(m.Name)))
		w.mu.Unlock()
		if kind == "blocked" {
			_ = w.H.Notify(title, sum+" — approve in its pane", sound)
		}
	default: // inbox
		_ = w.L.AddItem(ledger.Item{WS: m.Name, Kind: kind, Summary: sum})
		n := len(w.L.Pending())
		_ = w.H.Notify(title, fmt.Sprintf("%s\ninbox (%d): %s", sum, n, w.P.Config.Keys.Inbox), sound)
	}
}

// closeouts notices a worker's closeout draft (→ inbox for approval) and, once the worker
// marks it applied, closes the workstream: worktrees removed, pane closed, folder archived.
func (w *Watcher) closeouts() {
	for _, m := range w.L.List(false) {
		if m.Phase == "" {
			continue
		}
		body, status, mod := w.L.Closeout(m.Name)
		st := w.L.Status(m.Name)
		if !mod.After(st.CloseoutMod) {
			continue
		}
		st.CloseoutMod = mod
		_ = w.L.SaveStatus(m.Name, st)
		switch {
		case m.Phase == "closeout" && status == "proposed":
			go func(m *ledger.Meta, final string) {
				s := &summarize.Summarizer{URL: w.P.Config.Summarizer.URL, Model: w.P.Config.Summarizer.Model, Enabled: w.P.Config.SummarizerEnabled()}
				sum, err := s.Summarize(m.Name, "closeout", final, "")
				if err != nil || sum == "" {
					sum = firstContentLine(final)
				}
				if sum == "" {
					sum = "closeout drafted; review and approve"
				}
				w.post(m, "closeout", sum, "", false)
			}(m, ledger.Section(body, "Final report"))
		case m.Phase == "applying" && status == "applied":
			if err := workstream.Close(w.P, w.L, w.H, m, false); err != nil {
				w.post(m, "question", "closeout applied, but: "+err.Error(), "", false)
				continue
			}
			w.Log.Printf("%s: closed out and archived", m.Name)
			_ = w.H.Notify(w.P.Config.Name+" · "+m.Name+"  🗄 closed", "folded into "+w.P.Config.Docs.Dir+" and archived", "none")
		}
	}
}

// hygiene suggests closing out (or splitting) workstreams that have been open too long or
// have had too many turns, and warns once when too many are open.
func (w *Watcher) hygiene() {
	if time.Since(w.lastHyg) < 5*time.Minute {
		return
	}
	w.lastHyg = time.Now()
	hc := w.P.Config.Hygiene
	open := w.L.List(false)
	if hc.MaxOpen > 0 && len(open) > hc.MaxOpen && !w.warnedOpen {
		w.warnedOpen = true
		_ = w.H.Notify(w.P.Config.Name+": "+fmt.Sprint(len(open))+" open workstreams", "consider closing some out (`foreman ws finish <name>`)", "none")
	}
	for _, m := range open {
		st := w.L.Status(m.Name)
		if m.Phase != "" || !st.HygieneAt.IsZero() {
			continue
		}
		age := time.Since(m.Created)
		var why string
		switch {
		case hc.StaleDays > 0 && age > time.Duration(hc.StaleDays)*24*time.Hour:
			why = fmt.Sprintf("open %d days", int(age.Hours()/24))
		case hc.MaxTurns > 0 && st.Turns > hc.MaxTurns:
			why = fmt.Sprintf("%d turns", st.Turns)
		default:
			continue
		}
		st.HygieneAt = time.Now()
		_ = w.L.SaveStatus(m.Name, st)
		w.post(m, "stale", why+": close it out (f) or split the rest into a new workstream", "", false)
	}
}

func (w *Watcher) updateInboxCount() {
	n := len(w.L.Pending())
	if n == w.inboxN || w.spaceID == "" {
		return
	}
	w.inboxN = n
	(&space.Space{P: w.P, H: w.H, ID: w.spaceID}).SetInboxCount(n)
}

// flushRelay delivers queued worker events to the orchestrator once it is idle.
func (w *Watcher) flushRelay() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.relay) == 0 || w.P.Config.UI.Mode != "relay" || w.orchPane == "" {
		return
	}
	agents, err := w.H.Agents()
	if err != nil {
		return
	}
	for _, a := range agents {
		if a.PaneID == w.orchPane && (a.Status == "idle" || a.Status == "done") {
			msg := strings.Join(w.relay, "\n") + "\nTell me about these briefly; don't act on them unless I ask."
			if err := w.H.Prompt(a.PaneID, msg); err == nil {
				w.relay = nil
			} else {
				w.Log.Printf("relay: %v", err)
			}
			return
		}
	}
}

func reportFor(kind, report string) string {
	if kind == "done" || kind == "question" {
		return report
	}
	return ""
}

func firstContentLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if c := summarize.Clean(ln); c != "" {
			return c
		}
	}
	return ""
}

func fallbackSummary(kind, report string) string {
	if kind == "done" || kind == "question" {
		if c := firstContentLine(ledger.Section(report, "Summary")); c != "" {
			return c
		}
	}
	return strings.TrimSpace(strings.TrimLeft(labels[kind], "⛔❓✅·✖ "))
}

// promptOrHold sends a prompt now, or holds it until the agent is idle. Restored agents show
// up in herdr's list before the harness has finished loading, so the first try often fails.
func (w *Watcher) promptOrHold(pane, text string) {
	if err := w.H.Prompt(pane, text); err != nil {
		w.Log.Printf("prompt %s not delivered yet (%v); holding it until the agent is idle", pane, err)
		w.parked[pane] = text
	}
}

// bootMarker records which orchestrator terminal the last bootstrap saw. herdr gives panes
// new terminal ids when the server restarts, so a matching id means this watcher is merely
// being restarted inside the same server lifetime and must not re-bootstrap.
type bootMarker struct {
	Session    string `json:"session"`
	TerminalID string `json:"terminal_id"`
}

func (w *Watcher) markerPath() string { return w.P.Path("logs", "bootstrap.json") }

func (w *Watcher) orchTerminal() string {
	panes, err := w.H.Panes(w.spaceID)
	if err != nil {
		return ""
	}
	for _, pn := range panes {
		if pn.PaneID == w.orchPane {
			return pn.TerminalID
		}
	}
	return ""
}

// bootstrap waits briefly for herdr's native session restore, then starts the orchestrator
// fresh or nudges a resumed one to rehydrate, and restarts workers that did not come back.
func (w *Watcher) bootstrap(ctx context.Context) {
	started := time.Now()
	cfg := w.P.Config
	sp := &space.Space{P: w.P, H: w.H, ID: w.spaceID}
	pane, created, err := sp.EnsureOrchestrator()
	if err != nil {
		w.Log.Printf("bootstrap: orchestrator pane: %v", err)
		return
	}
	if created {
		w.Log.Printf("bootstrap: the orchestrator tab was missing; recreated it")
	}
	w.orchPane = pane
	term := w.orchTerminal()
	var mk bootMarker
	if b, err := os.ReadFile(w.markerPath()); err == nil && json.Unmarshal(b, &mk) == nil &&
		mk.Session == cfg.Herdr.Session && mk.TerminalID != "" && mk.TerminalID == term {
		w.Log.Printf("bootstrap: already done for this server lifetime")
		return
	}
	orchName := space.AgentName(w.P, cfg.Orchestrator.Name)
	resumed := w.waitForAgent(ctx, pane, 12*time.Second)
	docsNote := ""
	if _, err := os.Stat(w.P.DocsDir()); err != nil {
		docsNote = fmt.Sprintf(" The project docs folder (%s) doesn't exist yet: offer to seed it, as described under \"Project docs\" in ORCHESTRATOR.md.", w.P.DocsDir())
	}
	if resumed {
		w.Log.Printf("bootstrap: orchestrator resumed natively")
		_ = w.H.Rename(pane, orchName) // names don't always survive a restore
		w.promptOrHold(pane, "[foreman → orchestrator] Session resumed. Run `foreman digest --since 24h` and `foreman inbox --list` to refresh your picture of the project, then wait for me."+docsNote)
	} else {
		w.Log.Printf("bootstrap: starting a fresh orchestrator")
		_, pr := cfg.OrchestratorProfile()
		if err := w.H.StartAgent(orchName, pr.Harness, pane, pr.Args); err != nil {
			w.Log.Printf("start orchestrator: %v", err)
		} else {
			w.promptOrHold(pane, "[foreman → orchestrator] Read .foreman/ORCHESTRATOR.md and follow it. Then run `foreman digest --since 24h`, give me a 3-line picture of where things stand, and wait for instructions."+docsNote)
		}
	}
	_ = w.H.PaneToken(pane, "ws", space.OrchestratorTab)

	agents, _ := w.H.Agents()
	live := map[string]bool{}
	for _, a := range agents {
		live[a.PaneID] = true
	}
	for _, m := range w.L.List(false) {
		if m.Created.After(started) {
			continue // created while we were waiting above; `ws new` is starting it
		}
		// Alive means its own agent is in its own pane, not merely "some agent is there".
		if p, err := sp.WorkerPane(m); err == nil && live[p] {
			// Native resume restores the conversation, not an interrupted turn.
			if st := w.L.Status(m.Name); st.AgentStatus == "working" || st.AgentStatus == "blocked" {
				w.Log.Printf("bootstrap: %s resumed natively mid-task; nudging it", m.Name)
				w.promptOrHold(m.PaneID, "[foreman → "+m.Name+"] herdr restarted while you were working, so your last step was interrupted (any running command was killed). Check where you got to, carry on with the brief, and update your report when you finish or need input.")
			} else {
				w.Log.Printf("bootstrap: %s resumed natively", m.Name)
			}
			continue
		}
		w.Log.Printf("bootstrap: restarting %s", m.Name)
		if err := workstream.Restart(w.P, w.L, w.H, m); err != nil {
			w.Log.Printf("restart %s: %v", m.Name, err)
		}
	}
	views.Redock(w.P, w.H)
	b, _ := json.Marshal(bootMarker{Session: cfg.Herdr.Session, TerminalID: w.orchTerminal()})
	_ = os.WriteFile(w.markerPath(), b, 0o644)
}

func (w *Watcher) waitForAgent(ctx context.Context, pane string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if agents, err := w.H.Agents(); err == nil {
			for _, a := range agents {
				if a.PaneID == pane {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	return false
}
