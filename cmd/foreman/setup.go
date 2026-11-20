package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/assets"
	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/skills"
	"github.com/PVjammer/foreman/internal/types"
	"github.com/PVjammer/foreman/internal/watch"
)

// ---- init ----

type asker struct {
	in  *bufio.Reader
	tty bool
}

func newAsker(yes bool) *asker {
	fi, err := os.Stdin.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0 && !yes
	return &asker{in: bufio.NewReader(os.Stdin), tty: tty}
}

// ask prompts on a terminal and returns def otherwise (or on an empty answer).
func (a *asker) ask(q, def string, opts []string) string {
	if !a.tty {
		return def
	}
	for {
		hint := ""
		if len(opts) > 0 {
			hint = " (" + strings.Join(opts, "/") + ")"
		}
		fmt.Printf("%s%s [%s]: ", q, hint, def)
		line, err := a.in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil || line == "" {
			return def
		}
		if len(opts) == 0 || oneOf(line, opts) == nil {
			return line
		}
		fmt.Println("  choose one of", strings.Join(opts, ", "))
	}
}

func initCmd() *cobra.Command {
	var name, orch, worker, scratch, tracker, mode, docsDir, docsMode string
	var yes, force, noSkills bool
	var lf landingFlags
	c := &cobra.Command{
		Use:   "init",
		Short: "Make the current directory a foreman project (asks a few questions on a terminal)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, _ := os.Getwd()
			existing, _ := project.Load(root)
			if exists(filepath.Join(root, project.Dir, "config.toml")) && !force {
				return fmt.Errorf("already initialized (use --force to regenerate the guides and re-answer the questions)")
			}
			g := project.Global()
			cur := g
			if existing != nil && exists(filepath.Join(root, project.Dir, "config.toml")) {
				cur = existing.Config
			}
			a := newAsker(yes)
			flag := func(n string) bool { return cmd.Flags().Changed(n) }
			pick := func(n, val, q, def string, opts []string) string {
				if flag(n) {
					return val
				}
				return a.ask(q, def, opts)
			}
			if a.tty {
				fmt.Println("Setting up foreman in", root)
			}
			name = pick("name", name, "Project name (shown as its space in herdr)", or(cur.Name, filepath.Base(root)), nil)
			if other, ok := project.Registry()[name]; ok && other != root {
				return fmt.Errorf("project name %q is already used by %s; pass --name", name, other)
			}
			if a.tty && !flag("docs-mode") {
				fmt.Println("\nWorkstreams fold what they learn into a docs folder when they close out. Where should it live?")
				fmt.Println("  commit    in the project, committed with it")
				fmt.Println("  local     in the project, but kept out of git (.git/info/exclude; teammates never see it)")
				fmt.Println("  external  outside the project, under", filepath.Join(project.DataDir(), "projects"))
			}
			docsMode = pick("docs-mode", docsMode, "Docs mode", cur.Docs.Mode, project.DocsModes)
			if err := oneOf(docsMode, project.DocsModes); err != nil {
				return err
			}
			docsDir = pick("docs-dir", docsDir, "Docs folder name", cur.Docs.Dir, nil)
			tracker = pick("tracker", tracker, "Tracker", cur.Tracker.Kind, trackers)
			if err := oneOf(tracker, trackers); err != nil {
				return err
			}
			profiles := g.ProfileNames()
			orchDef, _ := cur.OrchestratorProfile()
			orch = pick("orchestrator", orch, "Orchestrator profile", orchDef, nil)
			worker = pick("worker", worker, "Default worker profile (the orchestrator can pick others per task)", cur.WorkerProfile(), nil)
			scratch = pick("scratch", scratch, "Scratch session profile (quick questions in a floating window)", cur.Scratch.Profile, nil)
			if a.tty {
				fmt.Printf("  (profiles: %s; edit them in %s)\n", strings.Join(profiles, ", "), project.GlobalConfigPath())
			}

			p := &project.Project{Root: root}
			for _, d := range []string{"ws", "logs", "types"} {
				if err := os.MkdirAll(p.Path(d), 0o755); err != nil {
					return err
				}
			}
			repos := project.DetectRepos(root)
			if existing != nil && len(existing.Config.Workers.Repos) > 0 {
				repos = existing.Config.Workers.Repos
			}
			landingDoc, forges, err := landingSetup(a, flag, root, repos, lf, cur)
			if err != nil {
				return err
			}
			doc := map[string]any{
				"name":    name,
				"workers": map[string]any{"repos": repos},
				"docs":    map[string]any{"dir": docsDir, "mode": docsMode},
				"tracker": map[string]any{"kind": tracker},
				"landing": landingDoc,
			}
			if len(forges["project"]) > 0 {
				doc["forges"] = forges["project"]
			}
			if orch != g.Orchestrator.Profile {
				doc["orchestrator"] = map[string]any{"profile": orch}
			}
			if worker != g.WorkerProfile() {
				doc["workers"].(map[string]any)["profile"] = worker
			}
			if scratch != g.Scratch.Profile {
				doc["scratch"] = map[string]any{"profile": scratch}
			}
			if flag("mode") {
				if err := oneOf(mode, modes); err != nil {
					return err
				}
				doc["ui"] = map[string]any{"mode": mode}
			}
			if err := project.WriteFile(root, doc); err != nil {
				return err
			}
			p, err = project.Load(root)
			if err != nil {
				return err
			}
			files := map[string]string{
				"ORCHESTRATOR.md": render("ORCHESTRATOR.md", map[string]any{"Root": root, "Name": name, "Repos": repos, "Docs": p.DocsDir()}),
				"tracker.md":      assets.File("tracker-" + tracker + ".md"),
				".gitignore":      "ws/\nlogs/\noutbox/\n*.jsonl\n*.tmp\n",
			}
			if !exists(p.Path("backlog.md")) {
				files["backlog.md"] = "# Backlog\n\nFollow-ups without a ticket. `foreman backlog add` appends here.\n\n"
			}
			for fn, body := range files {
				if err := os.WriteFile(p.Path(fn), []byte(body), 0o644); err != nil {
					return err
				}
			}
			switch docsMode {
			case "local":
				_ = project.Exclude(root, "/"+project.Dir+"/", "/"+strings.Trim(docsDir, "/")+"/")
			case "external":
				_ = project.Exclude(root, "/"+project.Dir+"/")
			}
			if err := project.Register(name, root); err != nil {
				return err
			}
			if err := saveForges(nil, map[string]map[string]any{"global": forges["global"]}); err != nil {
				return err
			}
			if !noSkills {
				if dirs, _ := skills.DirsFor(defaultHarnesses(&p.Config)); len(dirs) > 0 {
					fmt.Println("installing orchestrator skills:")
					if _, err := installSkills(p, dirs, false, false); err != nil {
						fmt.Println("  skills:", err)
					}
				}
			}
			if !exists(project.GlobalConfigPath()) {
				_ = os.MkdirAll(project.ConfigDir(), 0o755)
				if err := os.WriteFile(project.GlobalConfigPath(), []byte(assets.File("global.toml")), 0o644); err == nil {
					fmt.Println("wrote", project.GlobalConfigPath(), "(profiles and defaults for every project)")
				}
			}
			fmt.Printf("\nInitialized %s: %d repos, tracker %s, docs %s (%s)\n", name, len(repos), tracker, p.DocsDir(), docsMode)
			fmt.Println("  .foreman/config.toml   this project's settings (on top of", project.GlobalConfigPath()+")")
			fmt.Println("  .foreman/ORCHESTRATOR.md  how the orchestrator works; edit freely")
			fmt.Println("  .foreman/types/        custom workstream types (`foreman types copy feature` to start from one)")
			if !noSkills {
				fmt.Println("  orchestrator skills    `foreman skills` lists them; `foreman skills install --harness …` for another harness")
			}
			if !exists(filepath.Join(root, "AGENTS.md")) && !exists(filepath.Join(root, "CLAUDE.md")) {
				fmt.Println("  no AGENTS.md/CLAUDE.md at the root: add one so the orchestrator knows what this project is")
			}
			fmt.Println("next: `foreman setup-herdr` (once per machine), `foreman doctor`, then `foreman up`")
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "project name (default: directory name)")
	c.Flags().StringVar(&orch, "orchestrator", "", "orchestrator profile (or a bare harness name)")
	c.Flags().StringVar(&worker, "worker", "", "default worker profile (or a bare harness name)")
	c.Flags().StringVar(&scratch, "scratch", "", "scratch session profile (default claude)")
	c.Flags().StringVar(&tracker, "tracker", "", "ledger | jira | linear")
	c.Flags().StringVar(&mode, "mode", "", "notifier | inbox | relay")
	c.Flags().StringVar(&docsDir, "docs-dir", "", "docs folder name (default foreman-docs)")
	c.Flags().StringVar(&docsMode, "docs-mode", "", "commit | local | external")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask; use defaults for anything not given as a flag")
	lf.register(c)
	c.Flags().BoolVar(&force, "force", false, "re-run on an initialized project (regenerates ORCHESTRATOR.md and tracker.md)")
	c.Flags().BoolVar(&noSkills, "no-skills", false, "don't install the orchestrator skills (`foreman skills install` later)")
	return c
}

// ---- settings ----

func settingCmd(use, short, key string, opts []string, get func(*project.Config) string, after func(*project.Project, string) error) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Println(get(&p.Config))
				return nil
			}
			if err := oneOf(args[0], opts); err != nil {
				return err
			}
			if err := p.SetLocal(key, args[0]); err != nil {
				return err
			}
			if after != nil {
				return after(p, args[0])
			}
			return nil
		},
	}
}

func modeCmd() *cobra.Command {
	return settingCmd("mode [notifier|inbox|relay]", "Show or switch the UX candidate for this project (takes effect live)",
		"ui.mode", modes, func(c *project.Config) string { return c.UI.Mode }, nil)
}

func layoutCmd() *cobra.Command {
	return settingCmd("layout [tabs|panes]", "Show or switch where new workers go: a tab each, or zoomed panes in one tab",
		"ui.layout", layouts, func(c *project.Config) string { return c.UI.Layout }, nil)
}

func trackerCmd() *cobra.Command {
	return settingCmd("tracker [ledger|jira|linear]", "Show or switch how the orchestrator tracks work (rewrites .foreman/tracker.md)",
		"tracker.kind", trackers, func(c *project.Config) string { return c.Tracker.Kind },
		func(p *project.Project, v string) error {
			return os.WriteFile(p.Path("tracker.md"), []byte(assets.File("tracker-"+v+".md")), 0o644)
		})
}

func profilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List worker profiles (named harness + args) and what each is for",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := project.Global()
			if p, err := load(); err == nil {
				cfg = p.Config
			}
			orch, _ := cfg.OrchestratorProfile()
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "PROFILE\tHARNESS\tARGS\tUSE FOR")
			for _, n := range cfg.ProfileNames() {
				pr := cfg.Profiles[n]
				mark := ""
				if n == cfg.WorkerProfile() {
					mark += " (default worker)"
				}
				if n == orch {
					mark += " (orchestrator)"
				}
				fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", n, mark, pr.Harness, strings.Join(pr.Args, " "), pr.UseFor)
			}
			tw.Flush()
			fmt.Println("\nedit profiles in", project.GlobalConfigPath(), "(or [profiles.<name>] in .foreman/config.toml)")
			return nil
		},
	}
}

func typesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "types",
		Short: "List workstream types (built-in, global and this project's)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TYPE\tFROM\tWORKTREES\tPROFILE\tDESCRIPTION")
			for _, t := range types.List(p) {
				wt := "no"
				if t.Worktrees {
					wt = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Name, t.Source, wt, or(t.Profile, "default"), t.Description)
			}
			return tw.Flush()
		},
	}
	show := &cobra.Command{
		Use: "show <type>", Short: "Print a type's definition", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			t, err := types.Get(p, args[0])
			if err != nil {
				return err
			}
			src, _ := types.BuiltIn(t.Name)
			if t.Path != "" {
				b, _ := os.ReadFile(t.Path)
				src = string(b)
			}
			fmt.Printf("# %s (%s%s)\n\n%s", t.Name, t.Source, map[bool]string{true: ": " + t.Path, false: ""}[t.Path != ""], src)
			return nil
		},
	}
	var global bool
	var as string
	cp := &cobra.Command{
		Use: "copy <type>", Short: "Copy a type into .foreman/types (or the global types folder) to customize it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			t, err := types.Get(p, args[0])
			if err != nil {
				return err
			}
			src, _ := types.BuiltIn(t.Name)
			if t.Path != "" {
				b, _ := os.ReadFile(t.Path)
				src = string(b)
			}
			dir := p.Path("types")
			if global {
				dir = filepath.Join(project.ConfigDir(), "types")
			}
			dst := filepath.Join(dir, or(as, t.Name)+".md")
			if exists(dst) {
				return fmt.Errorf("%s already exists", dst)
			}
			_ = os.MkdirAll(dir, 0o755)
			if err := os.WriteFile(dst, []byte(src), 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", dst, "— edit it; it now shadows the", t.Source, "one")
			return nil
		},
	}
	cp.Flags().BoolVar(&global, "global", false, "copy into ~/.config/foreman/types for every project")
	cp.Flags().StringVar(&as, "as", "", "save under a new name (a custom type)")
	c.AddCommand(show, cp)
	return c
}

// ---- herdr config ----

const beginMark, endMark = "# --- foreman (begin) ---", "# --- foreman (end) ---"

var deliveryRe = regexp.MustCompile(`(?m)^delivery\s*=\s*"([a-z]+)"`)
var ourTabBarRe = regexp.MustCompile(`(?m)^tab_bar_right = .*# foreman\n`)

// mergeHerdrConfig puts foreman's block into a herdr config, keeping the user's own choices:
// an existing toast delivery setting survives and tables the user already defines are
// skipped. The block keeps its position when it is already there. It also removes the
// tab-bar supervisor line older versions added to the user's [ui] table (the plugin's hooks
// do that job now).
func mergeHerdrConfig(cur, exe string, keys project.Config) (string, []string) {
	var notes []string
	delivery := "herdr"
	before, after, hadBlock := cur, "", false
	if i, j := strings.Index(cur, beginMark), strings.Index(cur, endMark); i >= 0 && j > i {
		if m := deliveryRe.FindStringSubmatch(cur[i:j]); m != nil {
			delivery = m[1]
		}
		before, after, hadBlock = cur[:i], cur[j+len(endMark):], true
	}
	before, after = ourTabBarRe.ReplaceAllString(before, ""), ourTabBarRe.ReplaceAllString(after, "")
	outside := before + after
	has := func(t string) bool { return strings.Contains(outside, t) }
	for _, t := range []string{"[ui.toast]", "[ui.sidebar.agents]", "[ui.sidebar.spaces]"} {
		if has(t) {
			notes = append(notes, "your config already defines "+t+"; foreman's block leaves it out (merge by hand if you want foreman's version)")
		}
	}
	block := render("herdr.toml", map[string]any{
		"Exe": exe, "Keys": keys.Keys, "Delivery": delivery,
		"Toast": !has("[ui.toast]"), "Agents": !has("[ui.sidebar.agents]"), "Spaces": !has("[ui.sidebar.spaces]"),
	})
	if hadBlock {
		if rest := strings.TrimLeft(after, "\n"); rest != "" {
			after = "\n" + rest
		} else {
			after = ""
		}
		return strings.TrimRight(before, "\n") + "\n\n" + strings.TrimRight(block, "\n") + "\n" + after, notes
	}
	if before != "" && !strings.HasSuffix(before, "\n") {
		before += "\n"
	}
	return before + "\n" + block, notes
}

// writePlugin generates foreman's herdr plugin and links it into herdr.
func writePlugin(exe string) (string, error) {
	dir := filepath.Join(project.DataDir(), "herdr-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(render("herdr-plugin.toml", map[string]any{"Exe": exe})), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("herdr", "plugin", "link", dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("herdr plugin link %s: %s", dir, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// runSetupHerdr merges foreman's block into herdr's config and links the plugin.
func runSetupHerdr(print bool, key string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path := os.Getenv("HERDR_CONFIG_PATH")
	if path == "" {
		dir, _ := os.UserConfigDir()
		path = filepath.Join(dir, "herdr", "config.toml")
	}
	cfg := project.Global()
	if p, err := load(); err == nil {
		cfg = p.Config
	}
	if key != "" {
		cfg.Keys.Inbox = key
	}
	cur, _ := os.ReadFile(path)
	out, notes := mergeHerdrConfig(string(cur), exe, cfg)
	if print {
		fmt.Print(out)
		return nil
	}
	// Validate with herdr itself before touching the real file.
	tmp, err := os.CreateTemp("", "herdr-config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(out)
	tmp.Close()
	check := exec.Command("herdr", "config", "check")
	check.Env = append(os.Environ(), "HERDR_CONFIG_PATH="+tmp.Name())
	if res, err := check.CombinedOutput(); err != nil || !strings.Contains(string(res), "ok") {
		return fmt.Errorf("the merged config doesn't validate, so nothing was written:\n%s\n(see it with `foreman setup-herdr --print`)", strings.TrimSpace(string(res)))
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if len(cur) > 0 {
		_ = os.WriteFile(path+".foreman-bak", cur, 0o644)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Println("updated", path, "(backup: "+path+".foreman-bak)")
	dir, err := writePlugin(exe)
	if err != nil {
		return err
	}
	fmt.Println("linked foreman's herdr plugin from", dir, "(starts and wakes watchers; see `herdr plugin list`)")
	for _, n := range notes {
		fmt.Println("note:", n)
	}
	fmt.Println("running servers pick it up after `herdr server reload-config` or a restart")
	return nil
}

func setupHerdrCmd() *cobra.Command {
	var print bool
	var key string
	c := &cobra.Command{
		Use:   "setup-herdr",
		Short: "Add foreman's keybindings and sidebar rows to herdr's config, and link foreman's herdr plugin",
		RunE:  func(cmd *cobra.Command, args []string) error { return runSetupHerdr(print, key) },
	}
	c.Flags().BoolVar(&print, "print", false, "print the merged config instead of writing it")
	c.Flags().StringVar(&key, "key", "", "inbox popup keybinding (default from [keys] inbox)")
	return c
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check herdr, integrations, profiles, the summarizer and the project",
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := func(good bool, msg string) {
				mark := "✔"
				if !good {
					mark = "✖"
				}
				fmt.Println(mark, msg)
			}
			out, err := exec.Command("herdr", "--version").Output()
			ok(err == nil, "herdr: "+strings.TrimSpace(string(out)))
			if out, err := exec.Command("herdr", "integration", "status").Output(); err == nil {
				fmt.Println("  integrations (needed for native resume after restart; install with `herdr integration install <agent>`):")
				for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					for _, a := range []string{"claude:", "pi:", "opencode:", "codex:"} {
						if strings.HasPrefix(ln, a) {
							fmt.Println("   ", ln)
						}
					}
				}
			}
			ok(exists(project.GlobalConfigPath()), "global config "+project.GlobalConfigPath())
			p, err := load()
			if err != nil {
				ok(false, err.Error())
				return nil
			}
			cfg := p.Config
			ok(true, fmt.Sprintf("project %s at %s (session %s, mode %s, layout %s, tracker %s)", cfg.Name, p.Root, cfg.Herdr.Session, cfg.UI.Mode, cfg.UI.Layout, cfg.Tracker.Kind))
			ok(project.Registry()[cfg.Name] == p.Root, "registered as "+cfg.Name)
			ok(exists(filepath.Join(p.Root, "AGENTS.md")) || exists(filepath.Join(p.Root, "CLAUDE.md")), "root AGENTS.md / CLAUDE.md")
			ok(exists(p.DocsDir()), fmt.Sprintf("docs folder %s (%s)%s", p.DocsDir(), cfg.Docs.Mode, map[bool]string{true: "", false: " — the orchestrator offers to seed it on first run"}[exists(p.DocsDir())]))
			orchProf, orch := cfg.OrchestratorProfile()
			seen := map[string]bool{}
			check := func(label, prof string) {
				_, pr := cfg.Resolve(prof)
				if seen[label+pr.Harness] {
					return
				}
				seen[label+pr.Harness] = true
				_, err := exec.LookPath(pr.Harness)
				ok(err == nil, fmt.Sprintf("%s %s → %s on PATH", label, prof, pr.Harness))
			}
			_, err = exec.LookPath(orch.Harness)
			ok(err == nil, fmt.Sprintf("orchestrator %s → %s on PATH", orchProf, orch.Harness))
			check("default worker", cfg.WorkerProfile())
			for _, n := range cfg.ProfileNames() {
				if _, err := exec.LookPath(cfg.Profiles[n].Harness); err != nil {
					fmt.Printf("  (profile %s: %s not on PATH)\n", n, cfg.Profiles[n].Harness)
				}
			}
			if cfg.SummarizerEnabled() {
				resp, err := http.Get(strings.TrimRight(cfg.Summarizer.URL, "/") + "/api/tags")
				good := false
				if err == nil {
					var buf bytes.Buffer
					buf.ReadFrom(resp.Body)
					resp.Body.Close()
					good = strings.Contains(buf.String(), `"`+cfg.Summarizer.Model+`"`)
				}
				ok(good, "summarizer model "+cfg.Summarizer.Model+" at "+cfg.Summarizer.URL)
			}
			ok(watch.Running(p), "watcher running for this project")
			landingDoctor(p, func(good bool, msg string) {
				if good {
					ok(true, msg)
				} else {
					fmt.Println("!", msg, "(landing is optional; nothing else depends on it)")
				}
			})
			dirs, unknown := skills.DirsFor(defaultHarnesses(&cfg))
			for _, d := range dirs {
				var bad, edited []string
				for name, st := range skills.Status(p.Root, p.Path("skills.json"), d) {
					switch st {
					case skills.Modified:
						edited = append(edited, name)
					case skills.Current:
					default:
						bad = append(bad, name+" "+st)
					}
				}
				sort.Strings(bad)
				sort.Strings(edited)
				msg := "orchestrator skills in " + d
				if len(bad) > 0 {
					msg += ": " + strings.Join(bad, ", ") + " (`foreman skills install`)"
				}
				ok(len(bad) == 0, msg)
				if len(edited) > 0 {
					fmt.Printf("  edited locally, kept on install: %s\n", strings.Join(edited, ", "))
				}
				if d == skills.Dirs["pi"] {
					fmt.Println("  (pi loads project skills only once you trust the project; it asks on first start)")
				}
			}
			for _, h := range unknown {
				fmt.Printf("  orchestrator/scratch harness %s has no known skill dir; it can read skills with `foreman skills show <name>`\n", h)
			}
			dir, _ := os.UserConfigDir()
			hc, _ := os.ReadFile(filepath.Join(dir, "herdr", "config.toml"))
			ok(strings.Contains(string(hc), beginMark), "herdr config has foreman keybindings (`foreman setup-herdr`)")
			if _, err := exec.LookPath("claude"); err == nil {
				cs, _ := os.ReadFile(claudeSettingsPath())
				_, missing, _ := mergeClaudeSettings(cs, herdr.SocketPath(cfg.Herdr.Session))
				ok(len(missing) == 0, "Claude settings let agents run foreman (`foreman setup-claude`)")
			}
			pl, _ := exec.Command("herdr", "plugin", "list").CombinedOutput()
			ok(strings.Contains(string(pl), "foreman (foreman) enabled"), "foreman's herdr plugin is linked and enabled (`foreman setup-herdr`)")
			ks := cfg.Keys
			for _, k := range []string{ks.Inbox, ks.Next, ks.Orchestrator, ks.OrchFloat, ks.Scratch, ks.ScratchClear, ks.Terminal, ks.Dock, ks.New} {
				if taken[k] {
					fmt.Printf("  key %s is also one of herdr's own bindings; change it under [keys]\n", k)
				}
			}
			if s := string(hc); strings.Contains(s, beginMark) && (!strings.Contains(s, "--active") || strings.Contains(s, "status-bar\", interval")) {
				fmt.Println("  herdr config has foreman's old block; re-run `foreman setup-herdr`")
			}
			if old := legacySession(p); old != "" {
				fmt.Printf("  note: this project used to have its own herdr session (%s); it now lives in %q. Finish what's running there, then `herdr --session %s server stop`.\n", old, cfg.Herdr.Session, old)
			}
			return nil
		},
	}
}

// taken lists herdr 0.9.5's default prefix bindings, so doctor can flag clashes.
var taken = map[string]bool{}

func init() {
	for _, k := range strings.Fields("? b c e g h j k l n o p q r s v w x z tab minus shift+d shift+g shift+n shift+p shift+r shift+t shift+tab shift+w shift+x alt+g ctrl+k") {
		taken["prefix+"+k] = true
	}
}

// legacySession returns the per-project session name from a pre-0.2 config, if any.
func legacySession(p *project.Project) string {
	b, err := os.ReadFile(p.Path("config.toml"))
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`(?m)^session\s*=\s*'?"?([a-z0-9-]+)`).FindStringSubmatch(string(b)); m != nil && m[1] != p.Config.Herdr.Session {
		return m[1]
	}
	return ""
}
