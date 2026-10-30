// Package inbox is the popup TUI (candidate 2): a queue of worker events you can answer,
// approve, close out, hand to the orchestrator or dismiss without switching sessions.
package inbox

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PVjammer/foreman/internal/herdr"
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
	icons      = map[string]string{"blocked": "⛔", "question": "❓", "closeout": "📝", "done": "✅", "stale": "🧹", "turn": "·", "exited": "✖"}
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
	m := model{p: p, l: ledger.New(p), h: space.Client(p), input: ti}
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
				m.replyTo, m.keysMode, m.noteMode = "", false, false
				m.input.Blur()
				return m, nil
			case "enter":
				text := strings.TrimSpace(m.input.Value())
				m.replyTo = ""
				m.input.Blur()
				m.input.SetValue("")
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
