// Package space maps a project onto the shared herdr session: the project is a workspace
// (labelled with the project name), its first tab is the orchestrator, and workers live
// either in a tab each (layout "tabs") or as panes of one "workstreams" tab ("panes").
package space

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

const (
	OrchestratorTab = "orchestrator"
	WorkstreamsTab  = "workstreams"
	ScratchTab      = "scratch"
	TerminalTab     = "term"
	tokenSource     = "foreman"
)

// Reserved are tab labels foreman owns, so workstreams can't take them.
var Reserved = []string{OrchestratorTab, WorkstreamsTab, ScratchTab, TerminalTab}

// Tab finds a tab by label and returns it with its first pane ("" if there is none).
func (s *Space) Tab(label string) (herdr.Tab, herdr.Pane, error) {
	tabs, err := s.H.Tabs(s.ID)
	if err != nil {
		return herdr.Tab{}, herdr.Pane{}, err
	}
	for _, t := range tabs {
		if t.Label != label {
			continue
		}
		panes, err := s.H.Panes(s.ID)
		if err != nil {
			return t, herdr.Pane{}, err
		}
		for _, pn := range panes {
			if pn.TabID == t.ID {
				return t, pn, nil
			}
		}
		return t, herdr.Pane{}, nil
	}
	return herdr.Tab{}, herdr.Pane{}, nil
}

// EnsureTab finds the labelled tab or creates it (unfocused) at cwd. created reports a new one.
func (s *Space) EnsureTab(label, cwd string) (herdr.Pane, bool, error) {
	t, pn, err := s.Tab(label)
	if err != nil {
		return pn, false, err
	}
	if t.ID != "" && pn.PaneID != "" {
		return pn, false, nil
	}
	_, pane, err := s.H.CreateTab(s.ID, cwd, label, false)
	if err != nil {
		return herdr.Pane{}, false, err
	}
	_ = s.H.PaneToken(pane, "ws", label)
	panes, _ := s.H.Panes(s.ID)
	for _, p := range panes {
		if p.PaneID == pane {
			return p, true, nil
		}
	}
	return herdr.Pane{PaneID: pane}, true, nil
}

type Space struct {
	P  *project.Project
	H  *herdr.Client
	ID string // herdr workspace id; "" when the project isn't open
}

func Client(p *project.Project) *herdr.Client { return herdr.New(p.Config.Herdr.Session) }

// Find looks up the project's workspace without creating it.
func Find(p *project.Project, h *herdr.Client) (*Space, error) {
	ws, err := h.WorkspaceByLabel(p.Config.Name)
	if err != nil {
		return nil, err
	}
	return &Space{P: p, H: h, ID: ws.ID}, nil
}

// Ensure opens the project's workspace if needed. created reports whether it is new.
func Ensure(p *project.Project, h *herdr.Client, focus bool) (*Space, bool, error) {
	s, err := Find(p, h)
	if err != nil {
		return nil, false, err
	}
	if s.ID != "" {
		if focus {
			_ = h.FocusWorkspace(s.ID)
		}
		return s, false, nil
	}
	id, pane, err := h.CreateWorkspace(p.Root, p.Config.Name, focus)
	if err != nil {
		return nil, false, err
	}
	s.ID = id
	if tabs, err := h.Tabs(id); err == nil && len(tabs) > 0 {
		_ = h.RenameTab(tabs[0].ID, OrchestratorTab)
	}
	_ = h.PaneToken(pane, "ws", OrchestratorTab)
	return s, true, nil
}

// ErrNoOrchestrator means the project's orchestrator tab is gone (closed by hand, say).
var ErrNoOrchestrator = errors.New("the orchestrator tab is missing")

// OrchestratorPane is the first pane of the tab labelled "orchestrator". It never falls back
// to another tab: borrowing a worker's tab once put the orchestrator in a workstream's pane,
// and that workstream's messages then went to the orchestrator.
func (s *Space) OrchestratorPane() (string, error) {
	t, pn, err := s.Tab(OrchestratorTab)
	if err != nil {
		return "", err
	}
	if t.ID == "" || pn.PaneID == "" {
		return "", ErrNoOrchestrator
	}
	return pn.PaneID, nil
}

// EnsureOrchestrator returns the orchestrator pane, recreating its tab if it was closed.
func (s *Space) EnsureOrchestrator() (string, bool, error) {
	pane, err := s.OrchestratorPane()
	if err == nil || !errors.Is(err, ErrNoOrchestrator) {
		return pane, false, err
	}
	_, pane, err = s.H.CreateTab(s.ID, s.P.Root, OrchestratorTab, false)
	if err != nil {
		return "", false, err
	}
	_ = s.H.PaneToken(pane, "ws", OrchestratorTab)
	return pane, true, nil
}

// WorkerPane returns the pane that really holds workstream m's worker. A recorded pane id
// is trusted only if the pane still belongs to m: the agent in it carries m's agent name,
// or, when it has no named agent, the pane sits in m's own tab (or the shared workstreams
// tab). Anything else is refused, so foreman never prompts, focuses or closes a pane that
// now belongs to someone else.
func WorkerPane(p *project.Project, h *herdr.Client, m *ledger.Meta) (string, error) {
	sp, err := Find(p, h)
	if err != nil {
		return "", err
	}
	if sp.ID == "" {
		return "", fmt.Errorf("%s isn't open (run `foreman up`)", p.Config.Name)
	}
	return sp.WorkerPane(m)
}

// ErrPaneGone and ErrPaneTaken explain why a workstream has no usable pane.
var (
	ErrPaneGone  = errors.New("its pane is gone")
	ErrPaneTaken = errors.New("its pane now belongs to something else")
)

func (s *Space) WorkerPane(m *ledger.Meta) (string, error) {
	if m.PaneID == "" {
		return "", fmt.Errorf("%s: %w (run `foreman ws resume %s`)", m.Name, ErrPaneGone, m.Name)
	}
	panes, err := s.H.Panes(s.ID)
	if err != nil {
		return "", err
	}
	var pn herdr.Pane
	for _, x := range panes {
		if x.PaneID == m.PaneID {
			pn = x
		}
	}
	if pn.PaneID == "" {
		return "", fmt.Errorf("%s: %w (run `foreman ws resume %s`)", m.Name, ErrPaneGone, m.Name)
	}
	want := AgentName(s.P, m.Name)
	agents, err := s.H.Agents()
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if a.PaneID != pn.PaneID || a.Name == "" {
			continue
		}
		if a.Name == want {
			return pn.PaneID, nil
		}
		return "", fmt.Errorf("%s: %w (%s holds %s; run `foreman ws resume %s` to give it a new pane)", m.Name, ErrPaneTaken, pn.PaneID, a.Name, m.Name)
	}
	tabs, err := s.H.Tabs(s.ID)
	if err != nil {
		return "", err
	}
	for _, t := range tabs {
		if t.ID == pn.TabID && (t.Label == m.Name || t.Label == WorkstreamsTab) {
			return pn.PaneID, nil
		}
	}
	return "", fmt.Errorf("%s: %w (%s isn't in its tab; run `foreman ws resume %s`)", m.Name, ErrPaneTaken, pn.PaneID, m.Name)
}

// NewWorkerPane makes room for a worker and returns its tab and pane.
func (s *Space) NewWorkerPane(name, cwd string, focus bool) (string, string, error) {
	if s.P.Config.UI.Layout != "panes" {
		tab, pane, err := s.H.CreateTab(s.ID, cwd, name, focus)
		if err == nil {
			_ = s.H.PaneToken(pane, "ws", name)
		}
		return tab, pane, err
	}
	tabs, err := s.H.Tabs(s.ID)
	if err != nil {
		return "", "", err
	}
	for _, t := range tabs {
		if t.Label != WorkstreamsTab {
			continue
		}
		panes, err := s.H.Panes(s.ID)
		if err != nil {
			return "", "", err
		}
		var inTab []string
		for _, pn := range panes {
			if pn.TabID == t.ID {
				inTab = append(inTab, pn.PaneID)
			}
		}
		if len(inTab) == 0 {
			break
		}
		// Alternate split directions so the tiles stay roughly square.
		dir := "right"
		if len(inTab)%2 == 0 {
			dir = "down"
		}
		pane, err := s.H.Split(inTab[len(inTab)-1], dir, cwd)
		if err != nil {
			return "", "", err
		}
		_ = s.H.PaneToken(pane, "ws", name)
		if focus {
			_ = s.H.FocusTab(t.ID)
			_ = s.H.Zoom(pane)
		}
		return t.ID, pane, nil
	}
	tab, pane, err := s.H.CreateTab(s.ID, cwd, WorkstreamsTab, focus)
	if err == nil {
		_ = s.H.PaneToken(pane, "ws", name)
	}
	return tab, pane, err
}

// Focus switches to a worker; in the panes layout it also zooms it to full size.
func Focus(p *project.Project, h *herdr.Client, pane string) error {
	if err := h.FocusAgent(pane); err != nil {
		return err
	}
	if p.Config.UI.Layout == "panes" {
		return h.Zoom(pane)
	}
	return nil
}

// SetInboxCount shows the pending count on the project's row in the sidebar ($inbox).
func (s *Space) SetInboxCount(n int) {
	v := ""
	if n > 0 {
		v = fmt.Sprintf("📥%d", n)
	}
	_, _ = s.H.Run("workspace", "report-metadata", s.ID, "--source", tokenSource, "--token", "inbox="+v)
}

// AgentName is unique across the shared session: herdr agent names are session-wide, so two
// projects can each have an orchestrator and a workstream called "auth".
func AgentName(p *project.Project, name string) string {
	slug := strings.ReplaceAll(project.Slug(p.Config.Name), "-", "")
	if len(slug) > 10 {
		slug = slug[:10]
	}
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		slug = "p" + slug
	}
	n := slug + "-" + name
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.TrimRight(n, "-_")
}
