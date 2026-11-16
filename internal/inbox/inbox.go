// Package inbox is the popup TUI (candidate 2): a queue of worker events you can answer,
// approve, close out, hand to the orchestrator or dismiss without switching sessions.
package inbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/landing"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/views"
	"github.com/PVjammer/foreman/internal/workstream"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	selStyle   = lipgloss.NewStyle().Reverse(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	icons      = map[string]string{"blocked": "⛔", "question": "❓", "closeout": "📝", "done": "✅", "stale": "🧹", "turn": "·", "exited": "✖", "land": "🛬", "unmerged": "↩", "landed": "🛬"}
)

type model struct {
	p        *project.Project
	l        *ledger.Ledger
	h        *herdr.Client
	items    []ledger.Item
	cur      int
	preview  string
	input    textinput.Model
	replyTo  string
	keysMode bool
	noteMode bool
	keepMode bool                              // the input is a "keep working" note
	choices  map[string]map[int]landing.Choice // follow-up choices per landing workstream
	flash    string
	w, ht    int
}

type tickMsg struct{}
type previewMsg struct{ ws, text string }
type doneMsg struct{ flash string }

func Run(p *project.Project) error {
	ti := textinput.New()
	ti.Placeholder = "reply to the worker…"
	ti.CharLimit = 4000
	m := model{p: p, l: ledger.New(p), h: space.Client(p), input: ti, choices: map[string]map[int]landing.Choice{}}
	m.items = m.l.Pending()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(m.loadPreview(), tick()) }

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m model) selected() *ledger.Item {
	if m.cur >= 0 && m.cur < len(m.items) {
		return &m.items[m.cur]
	}
	return nil
}

// target resolves a workstream to its pane, refusing panes that no longer belong to it.
func (m model) target(ws string) (string, error) {
	meta, err := m.l.Meta(ws)
	if err != nil {
		return "", err
	}
	return space.WorkerPane(m.p, m.h, meta)
}

// withTarget runs fn on the workstream's verified pane.
func (m model) withTarget(ws string, fn func(pane string) error) error {
	pane, err := m.target(ws)
	if err != nil {
		return err
	}
	return fn(pane)
}

func (m model) loadPreview() tea.Cmd {
	it := m.selected()
	if it == nil {
		return nil
	}
	if it.Kind == "land" || it.Kind == "unmerged" {
		return m.landingPreview(*it)
	}
	return func() tea.Msg {
		var b strings.Builder
		if it.Kind == "closeout" {
			body, _, _ := m.l.Closeout(it.WS)
			for _, sec := range []string{"Final report", "Doc updates", "Tracker updates", "Follow-ups", "Branches and worktrees"} {
				if s := ledger.Section(body, sec); s != "" {
					fmt.Fprintf(&b, "%s\n%s\n\n", titleStyle.Render(sec), s)
				}
			}
			return previewMsg{it.WS, b.String()}
		}
		report, _, _ := m.l.Report(it.WS)
		if (it.Kind == "done" || it.Kind == "question" || it.Kind == "stale") && report != "" {
			for _, sec := range []string{"Summary", "Open questions", "Changes", "Follow-ups"} {
				if s := ledger.Section(report, sec); s != "" && s != "none" {
					fmt.Fprintf(&b, "%s\n%s\n\n", titleStyle.Render(sec), s)
				}
			}
		}
		if it.Kind != "done" || b.Len() == 0 {
			if pane, err := m.target(it.WS); err == nil {
				if screen, err := m.h.Read(pane, 30); err == nil {
					fmt.Fprintf(&b, "%s\n%s", titleStyle.Render("Terminal"), strings.TrimRight(screen, "\n "))
				}
			}
		}
		return previewMsg{it.WS, b.String()}
	}
}

func (m model) act(fn func(it ledger.Item) (string, error), ack bool) tea.Cmd {
	it := m.selected()
	if it == nil {
		return nil
	}
	item := *it
	return func() tea.Msg {
		msg, err := fn(item)
		if err != nil {
			return doneMsg{"error: " + err.Error()}
		}
		if ack {
			_ = m.l.Ack(item.ID)
		}
		return doneMsg{msg}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.ht = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		before := ""
		if it := m.selected(); it != nil {
			before = it.ID
		}
		m.items = m.l.Pending()
		if m.cur >= len(m.items) {
			m.cur = max(0, len(m.items)-1)
		}
		var cmd tea.Cmd
		if it := m.selected(); it != nil && it.ID != before {
			cmd = m.loadPreview()
		}
		return m, tea.Batch(cmd, tick())
	case previewMsg:
		if it := m.selected(); it != nil && it.WS == msg.ws {
			m.preview = msg.text
		}
		return m, nil
	case doneMsg:
		m.flash = msg.flash
		m.items = m.l.Pending()
		if m.cur >= len(m.items) {
			m.cur = max(0, len(m.items)-1)
		}
		return m, m.loadPreview()
	case tea.KeyMsg:
		if m.replyTo != "" {
			switch msg.String() {
			case "esc":
				m.replyTo, m.keysMode, m.noteMode, m.keepMode = "", false, false, false
				m.input.Blur()
				return m, nil
			case "enter":
				text := strings.TrimSpace(m.input.Value())
				m.replyTo = ""
				m.input.Blur()
				m.input.SetValue("")
				if m.keepMode {
					m.keepMode = false
					if text == "" {
						return m, nil
					}
					return m, m.act(func(it ledger.Item) (string, error) {
						meta, err := m.l.Meta(it.WS)
						if err != nil {
							return "", err
						}
						return "sent " + it.WS + " back to work", m.env().Keep(meta, text)
					}, true)
				}
				if m.noteMode {
					m.noteMode = false
					return m, m.act(func(it ledger.Item) (string, error) {
						return "closeout approved for " + it.WS, m.approve(it.WS, text)
					}, true)
				}
				if text == "" {
					return m, nil
				}
				if m.keysMode {
					m.keysMode = false
					return m, m.act(func(it ledger.Item) (string, error) {
						return "sent keys to " + it.WS, m.withTarget(it.WS, func(pn string) error { return m.h.SendKeys(pn, strings.Fields(text)...) })
					}, false)
				}
				return m, m.act(func(it ledger.Item) (string, error) {
					return "sent to " + it.WS, m.withTarget(it.WS, func(pn string) error { return m.h.Prompt(pn, text) })
				}, true)
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cur < len(m.items)-1 {
				m.cur++
				m.preview = ""
				return m, m.loadPreview()
			}
		case "k", "up":
			if m.cur > 0 {
				m.cur--
				m.preview = ""
				return m, m.loadPreview()
			}
		case "enter":
			// Blocked workers and questions: answer in the live terminal, right here.
			// ctrl+b q comes back to the inbox.
			if it := m.selected(); it != nil && (it.Kind == "blocked" || it.Kind == "question" || it.Kind == "stale") {
				item := *it
				pane, err := m.target(item.WS)
				if err != nil {
					m.flash = "error: " + err.Error()
					return m, nil
				}
				argv := views.AttachArgv(m.h, views.Target{Pane: herdr.Pane{PaneID: pane}, Agent: true})
				return m, tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
					if err != nil {
						return doneMsg{"attach " + item.WS + ": " + err.Error()}
					}
					_ = m.l.Ack(item.ID)
					return doneMsg{"back from " + item.WS}
				})
			}
			fallthrough
		case "o":
			if it := m.selected(); it != nil {
				pane, err := m.target(it.WS)
				if err != nil {
					m.flash = "error: " + err.Error()
					return m, nil
				}
				_ = m.l.Ack(it.ID)
				_ = space.Focus(m.p, m.h, pane)
				return m, tea.Quit
			}
		case "v":
			return m, m.act(func(it ledger.Item) (string, error) {
				_, err := views.DockIt(m.p, m.h, it.WS)
				return "docked " + it.WS + " beside the orchestrator", err
			}, false)
		case "f":
			return m, m.act(func(it ledger.Item) (string, error) {
				meta, err := m.l.Meta(it.WS)
				if err != nil {
					return "", err
				}
				return "asked " + it.WS + " to draft its closeout", workstream.Finish(m.p, m.l, m.h, meta)
			}, true)
		case "a":
			if it := m.selected(); it != nil && it.Kind == "closeout" {
				m.replyTo, m.noteMode = it.WS, true
				m.input.Placeholder = "optional change to apply with it (enter to approve as is)"
				m.input.Focus()
				return m, textinput.Blink
			}
		case "r":
			if it := m.selected(); it != nil {
				m.replyTo = it.WS
				m.input.Placeholder = "reply to the worker…"
				m.input.Focus()
				return m, textinput.Blink
			}
		case "y":
			return m, m.act(func(it ledger.Item) (string, error) {
				return "pressed enter on the highlighted option in " + it.WS, m.withTarget(it.WS, func(pn string) error { return m.h.SendKeys(pn, "enter") })
			}, true)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			digit := msg.String()
			if it := m.selected(); it != nil && it.Kind == "land" {
				// On a ready-to-land item, digits cycle that follow-up's choice.
				n := int(digit[0] - '1')
				ch := m.choices[it.WS]
				if ch == nil {
					ch = map[int]landing.Choice{}
					m.choices[it.WS] = ch
				}
				ch[n] = nextChoice(ch[n])
				return m, m.loadPreview()
			}
			return m, m.act(func(it ledger.Item) (string, error) {
				return "sent " + digit + " to " + it.WS, m.withTarget(it.WS, func(pn string) error { return m.h.SendKeys(pn, digit) })
			}, true)
		case "s":
			if it := m.selected(); it != nil {
				m.replyTo, m.keysMode = it.WS, true
				m.input.Placeholder = "keys, e.g. down enter"
				m.input.Focus()
				return m, textinput.Blink
			}
		case "x":
			return m, m.act(func(it ledger.Item) (string, error) {
				return "rejected (esc) " + it.WS, m.withTarget(it.WS, func(pn string) error { return m.h.SendKeys(pn, "esc") })
			}, true)
		case "h":
			return m, m.act(func(it ledger.Item) (string, error) {
				sp, err := space.Find(m.p, m.h)
				if err != nil || sp.ID == "" {
					return "", fmt.Errorf("project workspace not open")
				}
				orch, err := sp.OrchestratorPane()
				if err != nil {
					return "", err
				}
				text := fmt.Sprintf("[foreman → orchestrator] The user handed you this: workstream %s is %s — %s. Look at it (`foreman ws show %s`) and tell me what you suggest.", it.WS, it.Kind, it.Summary, it.WS)
				return "handed to the orchestrator", m.h.Prompt(orch, text)
			}, true)
		case "L":
			if it := m.selected(); it != nil && it.Kind == "land" {
				ch := m.choices[it.WS]
				return m, m.act(func(it ledger.Item) (string, error) {
					meta, err := m.l.Meta(it.WS)
					if err != nil {
						return "", err
					}
					out, err := m.env().Land(meta, ch)
					if errors.Is(err, landing.ErrHandedBack) {
						return "not landed, sent back to the worker: " + strings.TrimPrefix(err.Error(), landing.ErrHandedBack.Error()+": "), nil
					}
					return "landed " + it.WS + ": " + out, err
				}, false)
			}
		case "P":
			if it := m.selected(); it != nil && (it.Kind == "land" || it.Kind == "unmerged") {
				return m, m.act(func(it ledger.Item) (string, error) {
					meta, err := m.l.Meta(it.WS)
					if err != nil {
						return "", err
					}
					return "parked " + it.WS + " (`foreman ws land " + it.WS + "` later)", m.env().Park(meta)
				}, true)
			}
		case "W":
			if it := m.selected(); it != nil && (it.Kind == "land" || it.Kind == "unmerged") {
				m.replyTo, m.keepMode = it.WS, true
				m.input.Placeholder = "what to change (e.g. paste the review comments)"
				m.input.Focus()
				return m, textinput.Blink
			}
		case "C":
			if it := m.selected(); it != nil && (it.Kind == "land" || it.Kind == "unmerged") {
				return m, m.act(func(it ledger.Item) (string, error) {
					meta, err := m.l.Meta(it.WS)
					if err != nil {
						return "", err
					}
					return "closed " + it.WS + " without landing (branch kept)", workstream.Close(m.p, m.l, m.h, meta, false)
				}, true)
			}
		case "g":
			if it := m.selected(); it != nil && (it.Kind == "land" || it.Kind == "unmerged") {
				if c := m.diffCmd(it.WS); c != nil {
					return m, tea.ExecProcess(c, func(err error) tea.Msg { return doneMsg{""} })
				}
			}
		case "d":
			return m, m.act(func(it ledger.Item) (string, error) { return "dismissed " + it.WS, nil }, true)
		case "D":
			for _, it := range m.items {
				_ = m.l.Ack(it.ID)
			}
			return m, func() tea.Msg { return doneMsg{"cleared"} }
		}
	}
	return m, nil
}

func (m model) View() string {
	if len(m.items) == 0 {
		return boxStyle.Render("Inbox empty.  "+dimStyle.Render("q to close")) + "\n" + m.flash
	}
	leftW := min(44, max(28, m.w*2/5))
	var list strings.Builder
	list.WriteString(titleStyle.Render(fmt.Sprintf("%s inbox (%d)", m.p.Config.Name, len(m.items))) + "\n\n")
	for i, it := range m.items {
		line := fmt.Sprintf("%s %-16s %s", icons[it.Kind], trunc(it.WS, 16), ago(it.Time))
		if i == m.cur {
			line = selStyle.Render(line)
		}
		list.WriteString(line + "\n")
		list.WriteString("   " + dimStyle.Render(trunc(it.Summary, leftW-4)) + "\n")
	}
	rightW := max(20, m.w-leftW-4)
	left := lipgloss.NewStyle().Width(leftW).Render(list.String())
	right := lipgloss.NewStyle().Width(rightW).MaxHeight(max(5, m.ht-6)).Render(m.preview)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)

	if it := m.selected(); it != nil && (it.Kind == "land" || it.Kind == "unmerged") {
		help := "L land · 1-9 cycle a follow-up (queue → start → start autonomous → drop) · W keep working · P park · C close without landing · g diff · d dismiss · ↑↓ move · q close"
		if it.Kind == "unmerged" {
			help = "W keep working (same branch) · P park · C close · g diff · d dismiss · ↑↓ move · q close"
		}
		footer := dimStyle.Width(max(20, m.w-2)).Render(help)
		if m.replyTo != "" {
			footer = "keep working on " + m.replyTo + ": " + m.input.View()
		} else if m.flash != "" {
			footer = m.flash + "\n" + footer
		}
		return body + "\n\n" + footer
	}
	help := "↑↓ move · enter answer live (blocked/questions; ctrl+b q returns) or open · o open session · v dock · r reply · y enter on highlighted · 1-9 pick option · s send keys · x esc · f close out · a approve closeout · h hand to orchestrator · d dismiss · D clear · q close"
	footer := dimStyle.Width(max(20, m.w-2)).Render(help)
	if m.replyTo != "" {
		verb := "reply to "
		if m.keysMode {
			verb = "keys for "
		} else if m.noteMode {
			verb = "approve closeout of "
		}
		footer = verb + m.replyTo + ": " + m.input.View()
	} else if m.flash != "" {
		footer = m.flash + "\n" + footer
	}
	return body + "\n\n" + footer
}

func (m model) approve(ws, note string) error {
	meta, err := m.l.Meta(ws)
	if err != nil {
		return err
	}
	return workstream.Approve(m.p, m.l, m.h, meta, note)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// List prints pending items for scripts and the orchestrator.
func List(p *project.Project) string {
	items := ledger.New(p).Pending()
	if len(items) == 0 {
		return "inbox empty\n"
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s %-8s %-20s %s (%s)\n", icons[it.Kind], it.Kind, it.WS, it.Summary, ago(it.Time))
	}
	return b.String()
}

func (m model) env() *landing.Env { return workstream.LandingEnv(m.p, m.l, m.h, nil) }

// nextChoice cycles a follow-up: queue → start → start autonomous → drop → queue.
func nextChoice(c landing.Choice) landing.Choice {
	switch {
	case c.Action == "" || c.Action == "queue":
		return landing.Choice{Action: "start", Mode: "interactive"}
	case c.Action == "start" && c.Mode != "autonomous":
		return landing.Choice{Action: "start", Mode: "autonomous"}
	case c.Action == "start":
		return landing.Choice{Action: "drop"}
	}
	return landing.Choice{Action: "queue"}
}

func choiceLabel(c landing.Choice) string {
	switch c.Action {
	case "start":
		return "start (" + c.Mode + ")"
	case "drop":
		return "drop"
	}
	return "queue"
}

// landingPreview shows the Land decision: what lands where, the tests, follow-ups with the
// current choices, and the diff stat.
func (m model) landingPreview(it ledger.Item) tea.Cmd {
	choices := m.choices[it.WS]
	return func() tea.Msg {
		var b strings.Builder
		meta, err := m.l.Meta(it.WS)
		if err != nil {
			return previewMsg{it.WS, err.Error()}
		}
		pk, err := landing.ReadPacket(m.l, it.WS)
		if err != nil || pk == nil {
			return previewMsg{it.WS, fmt.Sprintf("landing packet: %v", err)}
		}
		fmt.Fprintf(&b, "%s\n%s\n\n", titleStyle.Render(pk.Title), pk.Summary)
		b.WriteString(titleStyle.Render("Lands as") + "\n")
		for _, wt := range meta.Worktrees {
			pol, ok := m.p.Config.LandingFor(wt.Repo)
			switch {
			case !ok:
				fmt.Fprintf(&b, "  %s: no landing policy (`foreman landing setup`)\n", wt.Repo)
			case pol.PR():
				fmt.Fprintf(&b, "  %s: push %s, PR into %s on %s/%s\n", wt.Repo, wt.Branch, pol.Base, pol.Forge, pol.Project)
			default:
				fmt.Fprintf(&b, "  %s: fast-forward local %s\n", wt.Repo, pol.Base)
			}
			if stat := diffStat(wt, pol.Base, pol.PR(), pol.Upstream); stat != "" {
				fmt.Fprintf(&b, "%s\n", dimStyle.Render(stat))
			}
		}
		if meta.Land != nil {
			for _, pr := range meta.Land.PRs {
				fmt.Fprintf(&b, "  %s: %s (%s)\n", pr.Repo, pr.URL, pr.State)
			}
		}
		if pk.Tests != "" {
			fmt.Fprintf(&b, "\n%s\n%s\n", titleStyle.Render("Tests"), pk.Tests)
		}
		if len(pk.Followups) > 0 && it.Kind == "land" {
			fmt.Fprintf(&b, "\n%s\n", titleStyle.Render("Follow-ups (press the number to change)"))
			for i, f := range pk.Followups {
				fmt.Fprintf(&b, "  %d [%s] %s (%s)\n", i+1, choiceLabel(choices[i]), f.Title, f.Type)
			}
		}
		if len(pk.DocsPatch) > 0 {
			fmt.Fprintf(&b, "\n%s applied to %s once landed\n", titleStyle.Render("Docs patch"), m.p.DocsDir())
		}
		return previewMsg{it.WS, b.String()}
	}
}

func diffStat(wt ledger.Worktree, base string, pr bool, upstream string) string {
	ref := base
	if pr {
		ref = upstream + "/" + base
	}
	out, err := exec.Command("git", "-C", wt.Path, "diff", "--shortstat", ref+"..."+wt.Branch).Output()
	if err != nil {
		out, _ = exec.Command("git", "-C", wt.Path, "diff", "--shortstat", base+"..."+wt.Branch).Output()
	}
	return "    " + strings.TrimSpace(string(out))
}

// diffCmd opens the workstream's diff against its base in git's pager.
func (m model) diffCmd(ws string) *exec.Cmd {
	meta, err := m.l.Meta(ws)
	if err != nil || len(meta.Worktrees) == 0 {
		return nil
	}
	wt := meta.Worktrees[0]
	pol, _ := m.p.Config.LandingFor(wt.Repo)
	base := pol.Base
	if base == "" {
		base = "main"
	}
	return exec.Command("git", "-C", wt.Path, "diff", base+"..."+wt.Branch)
}
