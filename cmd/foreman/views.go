package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/tui"
	"github.com/PVjammer/foreman/internal/views"
	"github.com/PVjammer/foreman/internal/watch"
	"github.com/PVjammer/foreman/internal/workstream"
)

// orchCmd switches to the orchestrator pane; pressed on the orchestrator, it goes back.
func orchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "orch",
		Short: "Switch to the orchestrator pane (run again to go back where you were)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			t, err := views.Resolve(e.p, e.h, views.Orchestrator)
			if err != nil {
				return err
			}
			back := e.p.Path("logs", "back.json")
			cur, curTab := os.Getenv("HERDR_ACTIVE_PANE_ID"), os.Getenv("HERDR_ACTIVE_TAB_ID")
			if cur == t.Pane.PaneID {
				var b struct{ Pane, Tab string }
				if raw, err := os.ReadFile(back); err == nil && json.Unmarshal(raw, &b) == nil {
					if e.h.FocusAgent(b.Pane) == nil {
						return nil
					}
					return e.h.FocusTab(b.Tab)
				}
				return nil
			}
			if cur != "" {
				raw, _ := json.Marshal(map[string]string{"Pane": cur, "Tab": curTab})
				_ = os.WriteFile(back, raw, 0o644)
			}
			return e.h.FocusAgent(t.Pane.PaneID)
		},
	}
}

// viewCmd shows a session live in the current terminal: a popup, or a dock pane.
func viewCmd() *cobra.Command {
	var docked bool
	c := &cobra.Command{
		Use:   "view <orchestrator|scratch|term|workstream>",
		Short: "Show a session live in this terminal (what the floating-window hotkeys run); close with ctrl+b q",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			target := args[0]
			if target == "orch" {
				target = views.Orchestrator
			}
			// A terminal has one writable view at a time. Floating something that is docked
			// would kick the dock out, so jump to the dock (it's beside the orchestrator).
			if !docked {
				if pane := views.DockedPane(e.p, e.h, target); pane != "" {
					if o, err := views.Resolve(e.p, e.h, views.Orchestrator); err == nil {
						_ = e.h.FocusAgent(o.Pane.PaneID)
					}
					return e.h.Notify(target+" is docked beside the orchestrator", "", "none")
				}
			}
			t, err := views.Resolve(e.p, e.h, target)
			if err != nil {
				fmt.Fprintln(os.Stderr, "foreman:", err)
				fmt.Fprintln(os.Stderr, "(press enter)")
				fmt.Scanln()
				return err
			}
			return views.Exec(e.h, t)
		},
	}
	c.Flags().BoolVar(&docked, "docked", false, "running inside a dock pane")
	return c
}

func dockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dock [scratch|term|workstream]",
		Short: "Dock a session beside the orchestrator (no argument: pick one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
			} else {
				marks := map[string]string{views.Scratch: "quick questions", views.Terminal: "shell at the project root"}
				for _, d := range views.Docks(e.p) {
					if views.DockedPane(e.p, e.h, d.Target) != "" {
						marks[d.Target] = "docked (choose to undock)"
					}
				}
				if target, err = tui.Pick("Dock beside the orchestrator", views.Choices(e.p), marks); err != nil || target == "" {
					return err
				}
				if views.DockedPane(e.p, e.h, target) != "" {
					return views.Undock(e.p, e.h, target)
				}
			}
			_, err = views.DockIt(e.p, e.h, target)
			return err
		},
	}
}

func undockCmd() *cobra.Command {
	return &cobra.Command{
		Use: "undock <target>", Short: "Close a dock (the session keeps running)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			return views.Undock(e.p, e.h, args[0])
		},
	}
}

func scratchCmd() *cobra.Command {
	c := &cobra.Command{Use: "scratch", Short: "The project's scratch session (open it with its hotkey or `foreman view scratch`)"}
	c.AddCommand(&cobra.Command{
		Use: "clear", Short: "End the scratch conversation; the next open starts fresh",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if err := views.ClearScratch(e.p, e.h); err != nil {
				return err
			}
			return e.h.Notify(e.p.Config.Name+": scratch cleared", "", "none")
		},
	})
	return c
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugName makes a workstream name from the first words of a request.
func slugName(text string, taken func(string) bool) string {
	words := strings.Fields(nonSlug.ReplaceAllString(strings.ToLower(text), " "))
	if len(words) > 4 {
		words = words[:4]
	}
	base := strings.Trim(strings.Join(words, "-"), "-")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "ws-" + base
	}
	if len(base) > 28 {
		base = strings.TrimRight(base[:28], "-")
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func requestCmd() *cobra.Command {
	var direct bool
	c := &cobra.Command{
		Use:   "request [what you want done]",
		Short: "Ask the orchestrator for a workstream (no argument: open a text box)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			text := strings.Join(args, " ")
			if text == "" {
				text, direct, err = tui.Compose("New workstream for "+e.p.Config.Name,
					"what should get done? (the orchestrator turns it into a brief)",
					"enter: send to the orchestrator · ctrl+d: start it directly as typed · ctrl+j: new line · esc: cancel")
				if err != nil || text == "" {
					return err
				}
			}
			if direct {
				name := slugName(text, func(n string) bool { _, err := e.l.Meta(n); return err == nil })
				m, err := workstream.Create(e.p, e.l, e.h, workstream.Spec{Name: name, Goal: text})
				if err != nil {
					return err
				}
				return e.h.Notify(e.p.Config.Name+" · "+m.Name+"  ▶ started", m.Goal, "none")
			}
			o, err := views.Resolve(e.p, e.h, views.Orchestrator)
			if err != nil {
				return err
			}
			msg := "[foreman → orchestrator] Workstream request from the user:\n\n" + text + "\n\n" +
				"Turn it into a workstream with the ws-start skill: write the goal like a ticket, pick the type, profile, repos and a short name, start it with `foreman ws new`, and tell me in one line what you started. Ask me first only if it's genuinely ambiguous."
			queued, err := views.SendOrQueue(e.p, e.h, o.Pane.PaneID, msg)
			if err != nil {
				return err
			}
			if queued {
				_ = watch.Wake(e.p)
				return e.h.Notify(e.p.Config.Name+": request queued", "the orchestrator is busy; it gets this when it's free", "none")
			}
			return e.h.Notify(e.p.Config.Name+": request sent to the orchestrator", firstLineOf(text), "none")
		},
	}
	c.Flags().BoolVar(&direct, "direct", false, "start the workstream as typed, without the orchestrator")
	return c
}

func firstLineOf(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }
