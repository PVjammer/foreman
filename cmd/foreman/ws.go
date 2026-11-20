package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/digest"
	"github.com/PVjammer/foreman/internal/inbox"
	"github.com/PVjammer/foreman/internal/landing"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/workstream"
)

// wsRun wraps the load-env-and-meta boilerplate shared by most ws subcommands.
func wsRun(fn func(e *env, m *ledger.Meta, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		e, err := loadEnv()
		if err != nil {
			return err
		}
		m, err := e.meta(args[0])
		if err != nil {
			return err
		}
		return fn(e, m, args[1:])
	}
}

// pane is the verified pane of a workstream (see space.WorkerPane).
func (e *env) pane(m *ledger.Meta) (string, error) { return space.WorkerPane(e.p, e.h, m) }

func wsCmd() *cobra.Command {
	c := &cobra.Command{Use: "ws", Short: "Manage workstreams"}

	var s workstream.Spec
	var repos, kind, harness string
	var dryRun bool
	newC := &cobra.Command{
		Use:   "new <name>",
		Short: "Start a workstream (worker agent + brief) in this project's space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if s.Goal == "" {
				return fmt.Errorf("--goal is required: it becomes the worker's brief")
			}
			s.Name = args[0]
			if s.Type == "" {
				s.Type = kind
			}
			if s.Profile == "" {
				s.Profile = harness
			}
			if repos != "" {
				s.Repos = strings.Split(repos, ",")
			}
			if dryRun {
				m, _, brief, err := workstream.Plan(e.p, e.l, s)
				if err != nil {
					return err
				}
				fmt.Printf("would start %s (%s, %s → %s) in %s\n", m.Name, m.Kind, m.Profile, m.Harness, m.Cwd)
				if m.Landing {
					fmt.Println("  ends by landing (project uses landing; --no-landing to close out instead)")
				}
				for _, wt := range m.Worktrees {
					fmt.Printf("  worktree %s → %s on branch %s\n", wt.Repo, wt.Path, wt.Branch)
				}
				fmt.Printf("nothing created (dry run). Brief:\n\n%s", brief)
				return nil
			}
			m, err := workstream.Create(e.p, e.l, e.h, s)
			if err != nil {
				return err
			}
			if s.Detached {
				fmt.Printf("created %s (%s) in %s without an agent\nbrief: %s\n", m.Name, m.Kind, m.Cwd, e.l.BriefPath(m.Name))
				return nil
			}
			fmt.Printf("started %s (%s, %s → %s) in %s\nbrief: %s\n", m.Name, m.Kind, m.Profile, m.Harness, m.Cwd, e.l.BriefPath(m.Name))
			_ = e.h.Notify(e.p.Config.Name+" · "+m.Name+"  ▶ started", firstLineOf(m.Goal), "none")
			return nil
		},
	}
	newC.Flags().StringVarP(&s.Type, "type", "t", "", "workstream type (`foreman types`; default from config)")
	newC.Flags().StringVarP(&s.Profile, "profile", "p", "", "worker profile (`foreman profiles`) or a bare harness name")
	newC.Flags().StringVar(&s.Goal, "goal", "", "what done looks like (becomes the brief)")
	newC.Flags().StringVar(&s.Ticket, "ticket", "", "tracker key")
	newC.Flags().StringVar(&repos, "repos", "", "comma-separated repos for worktrees (types with worktrees)")
	newC.Flags().BoolVar(&s.Focus, "focus", false, "switch to the new workstream")
	newC.Flags().BoolVar(&dryRun, "dry-run", false, "print the resolved type, profile, worktrees and brief; create nothing")
	newC.Flags().BoolVar(&s.NoLanding, "no-landing", false, "close out with `ws finish` → approve even if the project uses landing")
	newC.Flags().BoolVar(&s.Detached, "detached", false, "create it without starting an agent (scripts and tests)")
	_ = newC.Flags().MarkHidden("detached")
	newC.Flags().StringVar(&kind, "kind", "", "alias for --type")
	newC.Flags().StringVar(&harness, "harness", "", "alias for --profile")
	_ = newC.Flags().MarkHidden("kind")
	_ = newC.Flags().MarkHidden("harness")

	var all bool
	listC := &cobra.Command{
		Use:   "list",
		Short: "List workstreams with live state and summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			live := map[string]string{}
			if agents, err := e.h.Agents(); err == nil {
				for _, a := range agents {
					live[a.PaneID] = a.Status
				}
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tPROFILE\tAGENT\tLAST\tTICKET\tSUMMARY")
			for _, m := range e.l.List(all) {
				st := e.l.Status(m.Name)
				state := or(live[m.PaneID], "—")
				if m.Phase == "landing" && m.Land != nil {
					state = m.Land.State
				} else if m.Phase != "" {
					state += " (" + m.Phase + ")"
				}
				if m.Closed {
					state = "closed"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Kind, or(m.Profile, m.Harness), state, st.LastEvent, m.Ticket, st.Summary)
			}
			return tw.Flush()
		},
	}
	listC.Flags().BoolVar(&all, "all", false, "include closed (archived) workstreams")

	showC := &cobra.Command{
		Use: "show <name>", Short: "Show a workstream's setup, status, report and closeout", Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			st := e.l.Status(m.Name)
			fmt.Printf("# %s (%s, %s → %s)\nticket: %s\ncwd: %s\nagent: %s · last event: %s · %s · %d turns%s\nsummary: %s\n\n## Goal\n%s\n\n",
				m.Name, m.Kind, or(m.Profile, "—"), m.Harness, or(m.Ticket, "—"), m.Cwd, or(st.AgentStatus, "?"), or(st.LastEvent, "none"),
				st.Updated.Format("01-02 15:04"), st.Turns, map[bool]string{true: " · phase " + m.Phase, false: ""}[m.Phase != ""],
				or(st.Summary, "—"), m.Goal)
			for _, wt := range m.Worktrees {
				fmt.Printf("worktree %s → %s (%s)\n", wt.Repo, wt.Path, wt.Branch)
			}
			report, _, mod := e.l.Report(m.Name)
			if report == "" {
				fmt.Println("\n(no report yet)")
			} else {
				fmt.Printf("\n## Report (%s)\n%s\n", mod.Format("01-02 15:04"), report)
			}
			if co, cs, cmod := e.l.Closeout(m.Name); co != "" {
				fmt.Printf("\n## Closeout (%s, %s)\n%s\n", cs, cmod.Format("01-02 15:04"), co)
			}
			landingShow(e, m)
			return nil
		}),
	}

	var lines int
	peekC := &cobra.Command{
		Use: "peek <name>", Short: "Print the last lines of the worker's terminal", Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			pane, err := e.pane(m)
			if err != nil {
				return err
			}
			out, err := e.h.Read(pane, lines)
			fmt.Print(out)
			return err
		}),
	}
	peekC.Flags().IntVarP(&lines, "lines", "n", 60, "lines to read")

	sendC := &cobra.Command{
		Use: "send <name> <message>", Short: "Send a message to a worker", Args: cobra.MinimumNArgs(2),
		RunE: wsRun(func(e *env, m *ledger.Meta, rest []string) error {
			pane, err := e.pane(m)
			if err != nil {
				return err
			}
			return e.h.Prompt(pane, strings.Join(rest, " "))
		}),
	}

	keysC := &cobra.Command{
		Use: "keys <name> <key>...", Short: "Send key presses to a worker (answer a dialog: 1, enter, down, esc)", Args: cobra.MinimumNArgs(2),
		RunE: wsRun(func(e *env, m *ledger.Meta, rest []string) error {
			pane, err := e.pane(m)
			if err != nil {
				return err
			}
			return e.h.SendKeys(pane, rest...)
		}),
	}

	linkC := &cobra.Command{
		Use: "link <name> <ticket>", Short: "Attach a tracker ticket to a workstream", Args: cobra.ExactArgs(2),
		RunE: wsRun(func(e *env, m *ledger.Meta, rest []string) error {
			m.Ticket = rest[0]
			_ = e.l.AddEvent(ledger.Event{WS: m.Name, Kind: "linked", Summary: rest[0]})
			return e.l.SaveMeta(m)
		}),
	}

	finishC := &cobra.Command{
		Use:   "finish <name>",
		Short: "Finish: the worker prepares to land (landing projects) or drafts its closeout for approval",
		Args:  cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			if err := workstream.Finish(e.p, e.l, e.h, m); err != nil {
				return err
			}
			if landing.Applies(e.p, m) {
				fmt.Printf("asked %s to prepare landing; the inbox asks you to land it when it's ready\n", m.Name)
				return nil
			}
			fmt.Printf("asked %s to draft %s; it shows up in the inbox for approval\n", m.Name, e.l.CloseoutPath(m.Name))
			return nil
		}),
	}

	var note string
	approveC := &cobra.Command{
		Use:   "approve <name>",
		Short: "Approve a closeout: the worker applies it, then foreman closes and archives the workstream",
		Args:  cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			return workstream.Approve(e.p, e.l, e.h, m, note)
		}),
	}
	approveC.Flags().StringVar(&note, "note", "", "a change to make while applying it")

	var keepWT bool
	closeC := &cobra.Command{
		Use:   "close <name>",
		Short: "Close a workstream now, without a closeout (removes worktrees, keeps branches, archives)",
		Args:  cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			if err := workstream.Close(e.p, e.l, e.h, m, keepWT); err != nil {
				return err
			}
			if m.Land != nil {
				for _, pr := range m.Land.PRs {
					if pr.State == "open" {
						fmt.Printf("note: %s's PR is still open: %s (close it on the forge if it's abandoned)\n", m.Name, pr.URL)
					}
				}
			}
			return nil
		}),
	}
	closeC.Flags().BoolVar(&keepWT, "keep-worktrees", false, "leave its git worktrees in place")

	resumeC := &cobra.Command{
		Use: "resume <name>", Short: "Restart a workstream's agent from its brief and report", Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			return workstream.Restart(e.p, e.l, e.h, m)
		}),
	}

	focusC := &cobra.Command{
		Use: "focus <name>", Short: "Switch to a workstream", Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			pane, err := e.pane(m)
			if err != nil {
				return err
			}
			return space.Focus(e.p, e.h, pane)
		}),
	}

	c.AddCommand(newC, listC, showC, peekC, sendC, keysC, linkC, finishC, approveC, closeC, resumeC, focusC)
	c.AddCommand(landCmds()...)
	return c
}

func backlogCmd() *cobra.Command {
	c := &cobra.Command{Use: "backlog", Short: "Follow-ups without a ticket"}
	var ws string
	add := &cobra.Command{
		Use: "add <text>", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			return e.l.AddBacklog(strings.Join(args, " "), ws)
		},
	}
	add.Flags().StringVar(&ws, "ws", "", "workstream it came from")
	c.AddCommand(add)
	return c
}

func inboxCmd() *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Worker events waiting on you (popup TUI; --list for plain text)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if list {
				fmt.Print(inbox.List(p))
				return nil
			}
			return inbox.Run(p)
		},
	}
	c.Flags().BoolVar(&list, "list", false, "print pending items and exit")
	return c
}

func nextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Jump to the workstream that most needs you",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			pending := e.l.Pending()
			if len(pending) == 0 {
				return e.h.Notify(e.p.Config.Name+": inbox empty", "", "none")
			}
			it := pending[0]
			m, err := e.meta(it.WS)
			if err != nil {
				return err
			}
			pane, err := e.pane(m)
			if err != nil {
				return err
			}
			_ = e.l.Ack(it.ID)
			return space.Focus(e.p, e.h, pane)
		},
	}
}

func digestCmd() *cobra.Command {
	var since string
	c := &cobra.Command{
		Use:   "digest",
		Short: "Roll-up of workstreams, reports, closeouts, commits and backlog (input for status updates)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			t, err := digest.ParseSince(since)
			if err != nil {
				return err
			}
			fmt.Print(digest.Build(p, t))
			return nil
		},
	}
	c.Flags().StringVar(&since, "since", "24h", "period, e.g. 24h or 7d")
	return c
}
