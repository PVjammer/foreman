package workstream

import (
	"fmt"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/landing"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/views"
)

// PlanLine describes how one repo lands, for the brief and the landing prompt.
type PlanLine struct{ Repo, How, BaseRef string }

// landingData is the template data for the landing steps, or nil when the workstream
// doesn't land (landing off for the project, --no-landing, or nothing configured).
func landingData(p *project.Project, l *ledger.Ledger, m *ledger.Meta) map[string]any {
	if !landing.Applies(p, m) {
		return nil
	}
	var plan []PlanLine
	for _, wt := range m.Worktrees {
		pol, ok := p.Config.LandingFor(wt.Repo)
		switch {
		case !ok:
			plan = append(plan, PlanLine{wt.Repo, "no landing policy yet (the user runs `foreman landing setup`)", "main"})
		case pol.PR():
			where := pol.Project
			if pol.HeadOwner != "" {
				where += " from your fork"
			}
			plan = append(plan, PlanLine{wt.Repo, fmt.Sprintf("pull request into `%s` on %s/%s, reviewed on the forge", pol.Base, pol.Forge, where), pol.Upstream + "/" + pol.Base})
		default:
			plan = append(plan, PlanLine{wt.Repo, fmt.Sprintf("fast-forward of local `%s`", pol.Base), pol.Base})
		}
	}
	return map[string]any{"M": m, "Plan": plan, "OutDir": landing.OutDir(l, m.Name), "Docs": p.DocsDir(), "DocsMode": p.Config.Docs.Mode}
}

// LandingEnv wires the landing package to herdr and the ledger. notify may be nil: items
// then go straight to the inbox with a toast (CLI and inbox use); the watcher passes its own.
func LandingEnv(p *project.Project, l *ledger.Ledger, h *herdr.Client, notify func(m *ledger.Meta, kind, sum string)) *landing.Env {
	if notify == nil {
		notify = func(m *ledger.Meta, kind, sum string) {
			_ = l.AddItem(ledger.Item{WS: m.Name, Kind: kind, Summary: sum})
			_ = h.Notify(p.Config.Name+" · "+m.Name, sum, "none")
		}
	}
	return &landing.Env{
		P: p, L: l,
		Handback: func(m *ledger.Meta, msg string) error { return Resume(p, l, h, m, msg) },
		EndSession: func(m *ledger.Meta) {
			if pane, err := space.WorkerPane(p, h, m); err == nil {
				_ = h.ClosePane(pane)
			}
		},
		Close:  func(m *ledger.Meta) error { return Close(p, l, h, m, false) },
		Notify: notify,
		Orchestrator: func(text string) error {
			sp, err := space.Find(p, h)
			if err != nil || sp.ID == "" {
				return fmt.Errorf("project workspace not open")
			}
			orch, err := sp.OrchestratorPane()
			if err != nil {
				return err
			}
			return views.Send(p, h, orch, text)
		},
	}
}

// Resume gives a workstream's worker a message: straight into its live session if it still
// has one, otherwise in a restarted session (native resume where the harness has it, else
// from brief + report), after the usual resume kickoff.
func Resume(p *project.Project, l *ledger.Ledger, h *herdr.Client, m *ledger.Meta, msg string) error {
	if m.PaneID == "" {
		// Never launched (ws new --detached): park the message for whoever starts it.
		return l.SetKickoff(m.Name, msg)
	}
	if pane, err := space.WorkerPane(p, h, m); err == nil {
		if agents, err := h.Agents(); err == nil {
			for _, a := range agents {
				if a.PaneID == pane {
					return views.Send(p, h, pane, msg)
				}
			}
		}
		return startAndKickoff(p, l, h, m, kickoff(p, l, m, true)+"\n\n"+msg)
	}
	return launch(p, l, h, m, false, kickoff(p, l, m, true)+"\n\n"+msg)
}

// landingPrompt asks a landing worker to prepare its packet now (ws finish).
func landingPrompt(p *project.Project, l *ledger.Ledger, m *ledger.Meta) (string, error) {
	return render("landing.md", landingData(p, l, m))
}
