// foreman: one orchestrator conversation per project, with worker sessions on herdr.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/assets"
	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
)

var modes = []string{"notifier", "inbox", "relay"}
var trackers = []string{"ledger", "jira", "linear"}
var layouts = []string{"tabs", "panes"}

// active is set by --active: resolve the project from herdr's focused workspace instead of
// the cwd. Keybindings and the tab bar use it, since their cwd can belong to another project.
var active bool

func main() {
	root := &cobra.Command{Use: "foreman", Short: "Orchestrator-centric agent sessions on herdr", SilenceUsage: true}
	root.PersistentFlags().BoolVar(&active, "active", false, "use the project of herdr's focused workspace (for keybindings)")
	root.AddCommand(initCmd(), upCmd(), downCmd(), watchCmd(), statusBarCmd(), hookCmd(),
		modeCmd(), layoutCmd(), trackerCmd(), profilesCmd(), typesCmd(), setupHerdrCmd(), doctorCmd(),
		wsCmd(), backlogCmd(), proposalsCmd(), landingCmd(), inboxCmd(), nextCmd(), digestCmd(),
		orchCmd(), viewCmd(), dockCmd(), undockCmd(), scratchCmd(), requestCmd(),
		installCmd(), setupClaudeCmd(), skillsCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func load() (*project.Project, error) {
	if active {
		if p := byWorkspace(os.Getenv("HERDR_ACTIVE_WORKSPACE_ID")); p != nil {
			return p, nil
		}
	}
	wd, _ := os.Getwd()
	p, err := project.Find(wd)
	if err == nil {
		return p, nil
	}
	if id := os.Getenv("HERDR_WORKSPACE_ID"); id != "" {
		if p := byWorkspace(id); p != nil {
			return p, nil
		}
	}
	return nil, err
}

// byWorkspace maps a herdr workspace (by id, or the focused one when id is "") to the
// registered project whose name is its label.
func byWorkspace(id string) *project.Project {
	sess := herdr.CurrentSession()
	if sess == "" {
		sess = project.Global().Herdr.Session
	}
	wss, err := herdr.New(sess).Workspaces()
	if err != nil {
		return nil
	}
	for _, ws := range wss {
		if (id != "" && ws.ID == id) || (id == "" && ws.Focused) {
			if p, err := project.ByName(ws.Label); err == nil {
				return p
			}
		}
	}
	return nil
}

type env struct {
	p *project.Project
	l *ledger.Ledger
	h *herdr.Client
}

func loadEnv() (*env, error) {
	p, err := load()
	if err != nil {
		return nil, err
	}
	return &env{p, ledger.New(p), space.Client(p)}, nil
}

func (e *env) meta(name string) (*ledger.Meta, error) {
	m, err := e.l.Meta(name)
	if err != nil {
		return nil, fmt.Errorf("no workstream %q", name)
	}
	return m, nil
}

func oneOf(v string, opts []string) error {
	for _, o := range opts {
		if v == o {
			return nil
		}
	}
	return fmt.Errorf("%q: expected one of %s", v, strings.Join(opts, ", "))
}

func render(name string, data any) string {
	var buf bytes.Buffer
	if err := assets.Templates.ExecuteTemplate(&buf, name, data); err != nil {
		panic(err)
	}
	return buf.String()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
