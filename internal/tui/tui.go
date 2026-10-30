// Package tui holds the small popup screens foreman opens from hotkeys: a picker and a
// compose box. The inbox is its own package.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	title = lipgloss.NewStyle().Bold(true)
	sel   = lipgloss.NewStyle().Reverse(true)
	dim   = lipgloss.NewStyle().Faint(true)
)

type pick struct {
	title  string
	opts   []string
	marks  map[string]string
	cur    int
	chosen string
}

func (m *pick) Init() tea.Cmd { return nil }

func (m *pick) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "j", "down":
			m.cur = min(m.cur+1, len(m.opts)-1)
		case "k", "up":
			m.cur = max(m.cur-1, 0)
		case "enter":
			if len(m.opts) > 0 {
				m.chosen = m.opts[m.cur]
			}
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *pick) View() string {
	var b strings.Builder
	b.WriteString(title.Render(m.title) + "\n\n")
	for i, o := range m.opts {
		line := "  " + o
		if mk := m.marks[o]; mk != "" {
			line += "  " + dim.Render(mk)
		}
		if i == m.cur {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + dim.Render("↑↓ move · enter choose · esc cancel"))
	return b.String()
}

// Pick shows a list and returns the chosen option ("" if cancelled). marks annotate options.
func Pick(heading string, opts []string, marks map[string]string) (string, error) {
	m := &pick{title: heading, opts: opts, marks: marks}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return "", err
	}
	return m.chosen, nil
}

type compose struct {
	title, help string
	ta          textarea.Model
	text        string
	direct      bool
	done        bool
}

func (m *compose) Init() tea.Cmd { return textarea.Blink }

func (m *compose) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ta.SetWidth(max(20, msg.Width-4))
		m.ta.SetHeight(max(3, msg.Height-8))
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "enter", "ctrl+d":
			m.text, m.direct, m.done = strings.TrimSpace(m.ta.Value()), msg.String() == "ctrl+d", true
			return m, tea.Quit
		case "ctrl+j", "alt+enter":
			m.ta.InsertString("\n")
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *compose) View() string {
	return fmt.Sprintf("%s\n\n%s\n\n%s", title.Render(m.title), m.ta.View(), dim.Render(m.help))
}

// Compose shows a text box. direct reports ctrl+d instead of enter; text is "" if cancelled.
func Compose(heading, placeholder, help string) (text string, direct bool, err error) {
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = 8000
	ta.Focus()
	m := &compose{title: heading, help: help, ta: ta}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return "", false, err
	}
	if !m.done {
		return "", false, nil
	}
	return m.text, m.direct, nil
}
