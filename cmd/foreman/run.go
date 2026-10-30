package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/space"
	"github.com/PVjammer/foreman/internal/watch"
)

func upCmd() *cobra.Command {
	var keep, noAttach bool
	c := &cobra.Command{
		Use:   "up",
		Short: "Open the project as a space in herdr: orchestrator, workstreams and watcher",
		Long: `Open the project as a space (workspace) in the shared herdr session.

From a plain terminal this starts herdr if needed and attaches it; quitting stops herdr
and every open project, and the next ` + "`foreman up`" + ` (or plain ` + "`herdr`" + `) resumes them.
From inside herdr it just opens or focuses the project's space.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			cfg := p.Config
			inside := os.Getenv("HERDR_ENV") == "1"
			if cur := herdr.CurrentSession(); inside && cur != cfg.Herdr.Session {
				return fmt.Errorf("you're inside herdr session %q, but foreman projects live in %q; run `foreman up` from a plain terminal (or set [herdr] session)", cur, cfg.Herdr.Session)
			}
			if other, ok := project.Registry()[cfg.Name]; ok && other != p.Root {
				return fmt.Errorf("project name %q is registered to %s; set a different name in .foreman/config.toml", cfg.Name, other)
			}
			_ = project.Register(cfg.Name, p.Root)
			_ = os.MkdirAll(p.Path("logs"), 0o755)
			h := space.Client(p)
			if !h.ServerRunning() {
				_ = os.MkdirAll(project.DataDir(), 0o755)
				if err := h.StartServer(filepath.Join(project.DataDir(), "herdr-server.log")); err != nil {
					return err
				}
			}
			if _, _, err := space.Ensure(p, h, true); err != nil {
				return err
			}
			if err := watch.Spawn(p); err != nil {
				return fmt.Errorf("start watcher: %w", err)
			}
			if inside || noAttach {
				fmt.Printf("opened %s in herdr session %s (watcher log: %s)\n", cfg.Name, cfg.Herdr.Session, p.Path("logs", "watch.log"))
				return nil
			}
			attachErr := h.Attach()
			if cfg.StopOnExit() && !keep {
				fmt.Printf("foreman: stopping herdr session %s (every open project); `foreman up` or `herdr` resumes them\n", cfg.Herdr.Session)
				_ = h.StopServer()
			}
			return attachErr
		},
	}
	c.Flags().BoolVar(&keep, "keep", false, "leave herdr running after you quit (agents keep working)")
	c.Flags().BoolVar(&noAttach, "no-attach", false, "open the project without attaching the herdr UI")
	return c
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Close this project's space: its agents stop, its state stays, `foreman up` brings it back",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			sp, err := space.Find(e.p, e.h)
			if err != nil {
				return err
			}
			if sp.ID == "" {
				fmt.Println(e.p.Config.Name, "isn't open")
				return nil
			}
			n := len(e.l.List(false))
			if err := e.h.CloseWorkspace(sp.ID); err != nil {
				return err
			}
			_ = e.l.AddEvent(ledger.Event{WS: "", Kind: "down"})
			fmt.Printf("closed %s (%d open workstreams kept in the ledger; they restart on `foreman up`)\n", e.p.Config.Name, n)
			return nil
		},
	}
}

func watchCmd() *cobra.Command {
	var bootstrap, daemon bool
	c := &cobra.Command{
		Use:   "watch",
		Short: "Run the project's watcher in the foreground (`up` and the tab bar start it for you)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			release, ok, err := watch.Lock(p)
			if err != nil {
				return err
			}
			if !ok {
				if daemon {
					return nil
				}
				return fmt.Errorf("a watcher is already running for %s", p.Config.Name)
			}
			defer release()
			l := log.New(os.Stderr, "", log.LstdFlags)
			l.Printf("watcher start: %s (session %s, pid %d)", p.Config.Name, p.Config.Herdr.Session, os.Getpid())
			watch.New(p, l).Run(context.Background(), bootstrap)
			l.Printf("watcher stop: %s", p.Config.Name)
			return nil
		},
	}
	c.Flags().BoolVar(&bootstrap, "bootstrap", false, "start/rehydrate the orchestrator and restart workers (once per herdr server lifetime)")
	c.Flags().BoolVar(&daemon, "daemon", false, "exit quietly if a watcher is already running")
	return c
}

// ensureWatchers starts a watcher for every registered project with an open space in sess,
// and returns the active workspace's project (nil if none).
func ensureWatchers(sess, activeID string) *project.Project {
	wss, err := herdr.New(sess).Workspaces()
	if err != nil {
		return nil
	}
	reg := project.Registry()
	var act *project.Project
	for _, ws := range wss {
		root, ok := reg[ws.Label]
		if !ok {
			continue
		}
		p, err := project.Load(root)
		if err != nil || p.Config.Herdr.Session != sess {
			continue
		}
		_ = watch.Spawn(p)
		if ws.ID == activeID || (activeID == "" && ws.Focused) {
			act = p
		}
	}
	return act
}

// hookCmd is what foreman's herdr plugin runs (see templates/herdr-plugin.toml). Plugin
// commands get the event in HERDR_PLUGIN_EVENT_JSON but no HERDR_SESSION, so the session
// comes from the global config.
func hookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "hook <startup|event>",
		Short:  "Entry point for foreman's herdr plugin",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sess := project.Global().Herdr.Session
			switch args[0] {
			case "startup":
				ensureWatchers(sess, "")
				return nil
			case "event":
				return hookEvent(sess, os.Getenv("HERDR_PLUGIN_EVENT_JSON"))
			}
			return fmt.Errorf("unknown hook %q", args[0])
		},
	}
}

func hookEvent(sess, raw string) error {
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			WorkspaceID string `json:"workspace_id"`
			Workspace   struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return nil // not ours to judge; never fail a herdr hook loudly
	}
	label := ev.Data.Workspace.Label
	if label == "" && ev.Data.WorkspaceID != "" {
		wss, err := herdr.New(sess).Workspaces()
		if err != nil {
			return nil
		}
		for _, ws := range wss {
			if ws.ID == ev.Data.WorkspaceID {
				label = ws.Label
			}
		}
	}
	p, err := project.ByName(label)
	if err != nil || p.Config.Herdr.Session != sess {
		return nil
	}
	if watch.Wake(p) == nil || ev.Event == "workspace_closed" {
		return nil
	}
	return watch.Spawn(p) // no watcher listening: this hook doubles as its supervisor
}

// statusBarCmd is an optional tab-bar entry (herdr runs it on the server every few seconds):
// it prints the active project's inbox count and, like the plugin's startup hook, starts any
// missing watchers. Before the plugin existed it was foreman's only supervisor.
func statusBarCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "status-bar",
		Short:  "Tab-bar entry: keep watchers running for open projects and print the inbox count",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sess := herdr.CurrentSession()
			if sess == "" {
				sess = project.Global().Herdr.Session
			}
			out := ""
			if p := ensureWatchers(sess, os.Getenv("HERDR_ACTIVE_WORKSPACE_ID")); p != nil {
				if n := len(ledger.New(p).Pending()); n > 0 {
					out = fmt.Sprintf("📥 %d", n)
				}
			}
			fmt.Println(out)
			return nil
		},
	}
}
