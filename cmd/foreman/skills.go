package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/skills"
)

// defaultHarnesses are the harnesses of the sessions that run at the project root and use
// the skills: the orchestrator and the scratch session.
func defaultHarnesses(cfg *project.Config) []string {
	_, orch := cfg.OrchestratorProfile()
	_, scratch := cfg.Resolve(cfg.Scratch.Profile)
	return []string{orch.Harness, scratch.Harness}
}

// skillDirs resolves --harness (claude | pi | opencode | all, comma-separated) or the
// project's defaults into skill directories.
func skillDirs(cfg *project.Config, harness string) (dirs, unknown []string, err error) {
	hs := defaultHarnesses(cfg)
	switch harness {
	case "":
	case "all":
		hs = skills.Harnesses
	default:
		hs = strings.Split(harness, ",")
		for _, h := range hs {
			if _, ok := skills.Dirs[h]; !ok {
				return nil, nil, fmt.Errorf("--harness %q: expected claude, pi, opencode or all", h)
			}
		}
	}
	dirs, unknown = skills.DirsFor(hs)
	return dirs, unknown, nil
}

// installSkills installs into dirs and prints what changed; it returns how many files were
// kept because the user edited them.
func installSkills(p *project.Project, dirs []string, force, dryRun bool) (int, error) {
	acts, err := skills.Install(p.Root, p.Path("skills.json"), dirs, force, dryRun)
	kept, changed := 0, 0
	for _, a := range acts {
		if a.Do == "unchanged" {
			continue
		}
		changed++
		if strings.HasPrefix(a.Do, "kept") {
			kept++
		}
		fmt.Printf("  %-55s %s\n", a.Path, a.Do)
	}
	if changed == 0 && err == nil {
		fmt.Printf("  skills up to date in %s\n", strings.Join(dirs, ", "))
	}
	if err == nil && !dryRun && p.Config.Docs.Mode != "commit" {
		_ = project.Exclude(p.Root, skills.ExcludePatterns(dirs)...)
	}
	return kept, err
}

func skillsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "skills",
		Short: "Orchestrator skills (start, status, triage, closeout, docs, troubleshoot) and their install state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listSkills()
		},
	}
	c.AddCommand(&cobra.Command{
		Use: "list", Short: "List the skills and whether each harness dir has them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return listSkills() },
	})

	var harness string
	var force, dryRun bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the skills into this project's harness skill dirs (safe to re-run)",
		Long: `Install foreman's skills into the project's skill directories:
  claude            .claude/skills/
  pi, opencode      .agents/skills/

Without --harness, installs for the orchestrator's and scratch session's harnesses.
Re-running updates skills foreman installed earlier and leaves edited ones alone
(--force replaces them, keeping a .foreman-bak copy).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			dirs, unknown, err := skillDirs(&p.Config, harness)
			if err != nil {
				return err
			}
			for _, h := range unknown {
				fmt.Printf("  %s: no known skill dir; it can read them with `foreman skills show <name>`\n", h)
			}
			if len(dirs) == 0 {
				return nil
			}
			if dryRun {
				fmt.Println("dry run, nothing written:")
			}
			_, err = installSkills(p, dirs, force, dryRun)
			return err
		},
	}
	install.Flags().StringVar(&harness, "harness", "", "claude | pi | opencode | all, comma-separated (default: orchestrator and scratch harnesses)")
	install.Flags().BoolVar(&force, "force", false, "replace skills you edited (keeps a .foreman-bak copy)")
	install.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change; write nothing")
	c.AddCommand(install)

	c.AddCommand(&cobra.Command{
		Use: "show <name>", Short: "Print a skill (for harnesses that don't load skills themselves)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := skills.Show(args[0])
			if err != nil {
				return err
			}
			fmt.Print(s)
			return nil
		},
	})
	return c
}

func listSkills() error {
	p, _ := load() // the list works outside a project too, without install state
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	var dirs []string
	if p != nil {
		all, _ := skills.DirsFor(skills.Harnesses)
		dirs = all
		fmt.Fprintln(tw, "SKILL\t"+strings.Join(dirs, "\t"))
	}
	var status []map[string]string
	for _, d := range dirs {
		status = append(status, skills.Status(p.Root, p.Path("skills.json"), d))
	}
	for _, s := range skills.List() {
		row := s.Name
		for _, st := range status {
			row += "\t" + st[s.Name]
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()
	fmt.Println()
	for _, s := range skills.List() {
		desc := s.Description
		if i := strings.Index(desc, ". "); i > 0 {
			desc = desc[:i+1]
		}
		fmt.Printf("  %-22s %s\n", s.Name, desc)
	}
	if p != nil {
		fmt.Println("\n`foreman skills install` installs or updates them; `foreman skills show <name>` prints one.")
	}
	return nil
}
