package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/forge"
	"github.com/PVjammer/foreman/internal/landing"
	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/workstream"
)

// ---- ws land / park / keep / ready ----

// parseChoices turns --start 1,3 --drop 2 --mode 1=autonomous into choices by index.
func parseChoices(start, drop, modes string) (map[int]landing.Choice, error) {
	out := map[int]landing.Choice{}
	set := func(list, action string) error {
		for _, f := range strings.Split(list, ",") {
			if f = strings.TrimSpace(f); f == "" {
				continue
			}
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 {
				return fmt.Errorf("follow-up numbers start at 1: %q", f)
			}
			c := out[n-1]
			c.Action = action
			out[n-1] = c
		}
		return nil
	}
	if err := set(start, "start"); err != nil {
		return nil, err
	}
	if err := set(drop, "drop"); err != nil {
		return nil, err
	}
	for _, f := range strings.Split(modes, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		k, v, ok := strings.Cut(f, "=")
		n, err := strconv.Atoi(k)
		if !ok || err != nil || n < 1 || (v != "interactive" && v != "autonomous") {
			return nil, fmt.Errorf("--mode wants N=interactive|autonomous, got %q", f)
		}
		c := out[n-1]
		c.Mode = v
		out[n-1] = c
	}
	return out, nil
}

func landCmds() []*cobra.Command {
	var start, drop, modes string
	landC := &cobra.Command{
		Use:   "land <name>",
		Short: "Land a ready workstream: fast-forward or push + open a PR/MR, file follow-ups (only when the user says so)",
		Long: "Land a workstream whose worker prepared it (state ready, or parked).\n" +
			"Follow-ups are numbered as `foreman ws show` lists them; unlisted ones are queued.\n" +
			"Conflicts and failing gates go back to a worker; nothing is merged or pushed then.",
		Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			ch, err := parseChoices(start, drop, modes)
			if err != nil {
				return err
			}
			out, err := workstream.LandingEnv(e.p, e.l, e.h, nil).Land(m, ch)
			if errors.Is(err, landing.ErrHandedBack) {
				fmt.Println("not landed:", strings.TrimPrefix(err.Error(), landing.ErrHandedBack.Error()+": "))
				fmt.Println("sent back to the worker; it reports done again when fixed")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		}),
	}
	landC.Flags().StringVar(&start, "start", "", "follow-ups to start now, e.g. 1,3")
	landC.Flags().StringVar(&drop, "drop", "", "follow-ups to drop, e.g. 2")
	landC.Flags().StringVar(&modes, "mode", "", "mode per follow-up, e.g. 1=autonomous")

	parkC := &cobra.Command{
		Use: "park <name>", Short: "Keep a ready or unmerged workstream open and idle (branch and PR untouched, no nudges)", Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			if err := workstream.LandingEnv(e.p, e.l, e.h, nil).Park(m); err != nil {
				return err
			}
			fmt.Printf("parked %s; land it later with `foreman ws land %s`\n", m.Name, m.Name)
			return nil
		}),
	}

	keepC := &cobra.Command{
		Use: "keep <name> <what to change>", Short: "Keep working: restart a worker on the same branch with your note (e.g. review comments)",
		Args: cobra.MinimumNArgs(2),
		RunE: wsRun(func(e *env, m *ledger.Meta, rest []string) error {
			if err := workstream.LandingEnv(e.p, e.l, e.h, nil).Keep(m, strings.Join(rest, " ")); err != nil {
				return err
			}
			fmt.Printf("sent %s back to work; it asks to land again when done\n", m.Name)
			return nil
		}),
	}

	readyC := &cobra.Command{
		Use: "ready <name>", Short: "Check a landing worker's packet now and mark it ready to land (the watcher does this on done)",
		Args: cobra.ExactArgs(1),
		RunE: wsRun(func(e *env, m *ledger.Meta, _ []string) error {
			ok, why := workstream.LandingEnv(e.p, e.l, e.h, nil).CheckReady(m)
			if !ok {
				if why == "" {
					why = "not a landing workstream, or not working (phase " + or(m.Phase, "none") + ")"
				}
				return errors.New(why)
			}
			fmt.Printf("%s is ready to land\n", m.Name)
			return nil
		}),
	}
	return []*cobra.Command{landC, parkC, keepC, readyC}
}

// landingShow prints a workstream's landing state and packet for `ws show`.
func landingShow(e *env, m *ledger.Meta) {
	if !m.Landing {
		return
	}
	st := m.Land
	state := "working"
	if st != nil {
		state = st.State
	}
	fmt.Printf("\n## Landing (%s)\n", state)
	if st != nil {
		for _, pr := range st.PRs {
			fmt.Printf("- %s: %s %s (%s)\n", pr.Repo, pr.Branch, pr.URL, pr.State)
		}
		for _, r := range st.Merged {
			fmt.Printf("- %s: fast-forwarded\n", r)
		}
		if st.LastError != "" {
			fmt.Println("last problem:", st.LastError)
		}
	}
	pk, err := landing.ReadPacket(e.l, m.Name)
	if err != nil {
		fmt.Println("packet:", err)
		return
	}
	if pk == nil {
		fmt.Println("packet: not written yet (" + landing.PacketPath(e.l, m.Name) + ")")
		return
	}
	fmt.Println(landing.ReadySummary(e.p, m, pk))
	for i, f := range pk.Followups {
		fmt.Printf("  follow-up %d: %s [%s, %s] %s\n", i+1, f.Title, or(f.Type, "?"), f.Mode, firstLineOf(f.Goal))
	}
	if len(pk.DocsPatch) > 0 {
		fmt.Printf("  docs.patch: %d bytes for %s\n", len(pk.DocsPatch), e.p.DocsDir())
	}
	for _, s := range pk.Skipped {
		fmt.Println("  skipped:", s)
	}
}

// ---- proposals ----

func proposalsCmd() *cobra.Command {
	c := &cobra.Command{Use: "proposals", Short: "Follow-ups queued at landing (start, drop, or mark started)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tFROM\tTYPE\tMODE\tTITLE")
			for _, pr := range e.l.Proposals() {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", pr.ID, pr.Status, pr.From, or(pr.Type, "—"), pr.Mode, pr.Title)
			}
			return tw.Flush()
		}}
	update := func(use, short, status string, notify bool) *cobra.Command {
		var mode string
		cc := &cobra.Command{Use: use + " <id>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				for _, pr := range e.l.Proposals() {
					if pr.ID != args[0] {
						continue
					}
					pr.Status, pr.Time = status, time.Time{}
					if mode != "" {
						pr.Mode = mode
					}
					if err := e.l.AddProposal(pr); err != nil {
						return err
					}
					if notify {
						if err := workstream.LandingEnv(e.p, e.l, e.h, nil).Orchestrator(landing.StartMessage(pr)); err != nil {
							return fmt.Errorf("recorded, but couldn't reach the orchestrator: %w", err)
						}
						fmt.Println("asked the orchestrator to start it")
					}
					return nil
				}
				return fmt.Errorf("no proposal %q", args[0])
			}}
		if notify {
			cc.Flags().StringVar(&mode, "mode", "", "interactive | autonomous")
		}
		return cc
	}
	c.AddCommand(update("start", "Approve a proposal: the orchestrator writes its brief and starts it", "start", true),
		update("drop", "Drop a proposal", "dropped", false),
		update("started", "Mark a proposal as started (the orchestrator does this after `ws new`)", "started", false))
	return c
}

// ---- onboarding: [landing] and [forges] ----

// landingFlags are init's / `landing setup`'s non-interactive answers.
type landingFlags struct {
	enable, mode, base, gate, branch, tokenCmd, tokenEnv, api, save string
	draft                                                           bool
}

func (f *landingFlags) register(c *cobra.Command) {
	c.Flags().StringVar(&f.enable, "landing", "", "use landing: on | off (default off; off keeps closeout as before)")
	c.Flags().StringVar(&f.mode, "landing-mode", "", "how repos land: pr (push + PR/MR) | ff (local fast-forward)")
	c.Flags().StringVar(&f.base, "landing-base", "", "base branch (default: detected)")
	c.Flags().StringVar(&f.gate, "landing-gate", "", "ff only: command foreman runs before merging (default: detected)")
	c.Flags().StringVar(&f.branch, "landing-branch", "", "remote branch template (default fm/{name})")
	c.Flags().BoolVar(&f.draft, "landing-draft", false, "open PRs/MRs as drafts")
	c.Flags().StringVar(&f.tokenCmd, "forge-token-cmd", "", `command that prints the forge token, e.g. "gh auth token"`)
	c.Flags().StringVar(&f.tokenEnv, "forge-token-env", "", "environment variable holding the token (fallback)")
	c.Flags().StringVar(&f.api, "forge-api", "", "forge API base URL (default: from the remote's host)")
	c.Flags().StringVar(&f.save, "forge-save", "", "where to save the forge entry: project | global (default project)")
}

// repoFacts is what onboarding detects about one repo.
type repoFacts struct {
	repo, push, upstream string
	target, pushed       forge.Remote
	forgeOK              bool
	base, gate           string
}

func remoteURL(dir, name string) string {
	// The URL as configured (not get-url, which applies insteadOf rewrites): it names the forge.
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote."+name+".url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectRepo(root, repo string) repoFacts {
	dir := filepath.Join(root, repo)
	f := repoFacts{repo: repo, push: "origin", upstream: "origin"}
	if remoteURL(dir, "upstream") != "" && remoteURL(dir, "origin") != "" {
		f.upstream = "upstream" // fork layout: push to your fork, open the PR upstream
	}
	if r, err := forge.ParseRemote(remoteURL(dir, f.upstream)); err == nil {
		f.target, f.forgeOK = r, true
		if p, err := forge.ParseRemote(remoteURL(dir, f.push)); err == nil {
			f.pushed = p
		}
	}
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/"+f.upstream+"/HEAD").Output(); err == nil {
		f.base = strings.TrimPrefix(strings.TrimSpace(string(out)), f.upstream+"/")
	} else if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output(); err == nil {
		f.base = strings.TrimSpace(string(out))
	}
	if f.base == "" {
		f.base = "main"
	}
	f.gate = detectGate(dir)
	return f
}

func detectGate(dir string) string {
	has := func(n string) bool { return exists(filepath.Join(dir, n)) }
	switch {
	case has("go.mod"):
		return "go test ./..."
	case has("package.json"):
		if b, _ := os.ReadFile(filepath.Join(dir, "package.json")); strings.Contains(string(b), `"test"`) {
			return "npm test"
		}
	case has("Makefile"):
		if b, _ := os.ReadFile(filepath.Join(dir, "Makefile")); strings.Contains("\n"+string(b), "\ntest:") {
			return "make test"
		}
	case has("pyproject.toml"), has("pytest.ini"), has("setup.py"):
		return "python -m pytest -q"
	}
	return ""
}

// forkOwner is the owner of the pushed fork when it differs from the target's owner.
func (f repoFacts) forkOwner() string {
	if f.upstream == f.push || f.pushed.Path == "" {
		return ""
	}
	po, _, _ := strings.Cut(f.pushed.Path, "/")
	to, _, _ := strings.Cut(f.target.Path, "/")
	if po == to {
		return ""
	}
	return po
}

// landingSetup asks (or takes from flags) the landing questions for the given repos. It
// returns the [landing] table and the forge entries to save, keyed "project"/"global".
func landingSetup(a *asker, flagSet func(string) bool, root string, repos []string, fl landingFlags, cur project.Config) (map[string]any, map[string]map[string]any, error) {
	pick := func(n, val, q, def string, opts []string) string {
		if flagSet(n) {
			return val
		}
		return a.ask(q, def, opts)
	}
	def := "no"
	if cur.Landing.Enabled {
		def = "yes"
	}
	enable := ""
	switch {
	case flagSet("landing"):
		enable = map[string]string{"on": "yes", "off": "no", "yes": "yes", "no": "no", "true": "yes", "false": "no"}[fl.enable]
		if enable == "" {
			return nil, nil, fmt.Errorf("--landing wants on | off")
		}
	default:
		if a.tty {
			fmt.Println("\nLanding (optional): a finished workstream prepares its branch, then one press either")
			fmt.Println("  pushes it and opens a PR/MR (closed out once merged), or fast-forwards your local base branch.")
			fmt.Println("  Say no to keep today's closeout (`ws finish` → approve). You can turn it on later: `foreman landing setup`.")
		}
		enable = a.ask("Use landing for this project?", def, []string{"yes", "no"})
	}
	if enable != "yes" {
		return map[string]any{"enabled": false}, nil, nil
	}
	ctx := context.Background()
	reposDoc := map[string]any{}
	forges := map[string]map[string]any{}
	for _, repo := range repos {
		f := detectRepo(root, repo)
		curPol := cur.Landing.Repos[repo]
		modeDef := "ff"
		q := fmt.Sprintf("Repo %s: no forge remote found; land by fast-forwarding local %s", repo, f.base)
		opts := []string{"ff"}
		if f.forgeOK {
			where := f.target.Host + "/" + f.target.Path
			if o := f.forkOwner(); o != "" {
				where += " (from your fork " + f.pushed.Path + ")"
			}
			modeDef = "pr"
			q = fmt.Sprintf("Repo %s: pr = push and open a PR/MR on %s; ff = fast-forward local %s (personal projects)", repo, where, f.base)
			opts = []string{"pr", "ff", "off"}
		} else {
			opts = append(opts, "off")
		}
		if curPol.Mode != "" {
			modeDef = map[string]string{"mr": "pr"}[curPol.Mode]
			if modeDef == "" {
				modeDef = curPol.Mode
			}
		}
		mode := pick("landing-mode", fl.mode, q, modeDef, opts)
		if mode == "mr" {
			mode = "pr"
		}
		if mode == "off" {
			continue
		}
		if mode == "pr" && !f.forgeOK {
			return nil, nil, fmt.Errorf("repo %s has no forge remote (origin/upstream on a host), so it can't land by PR", repo)
		}
		base := pick("landing-base", fl.base, fmt.Sprintf("Repo %s: base branch", repo), or(curPol.Base, f.base), nil)
		pol := map[string]any{"mode": mode, "base": base}
		if mode == "ff" {
			if g := pick("landing-gate", fl.gate, fmt.Sprintf("Repo %s: command to run before merging (empty for none)", repo), or(curPol.Gate, f.gate), nil); g != "" {
				pol["gate"] = g
			}
			reposDoc[repo] = pol
			continue
		}
		pol["remote"] = f.push
		if f.upstream != f.push {
			pol["upstream"] = f.upstream
		}
		pol["forge"] = f.target.Host
		pol["project"] = f.target.Path
		if o := f.forkOwner(); o != "" {
			pol["head_owner"] = o
		}
		branch := pick("landing-branch", fl.branch, fmt.Sprintf("Repo %s: remote branch name ({name}, {ticket})", repo), or(curPol.Branch, "fm/{name}"), nil)
		if branch != "fm/{name}" {
			pol["branch"] = branch
		}
		draft := curPol.Draft
		if flagSet("landing-draft") {
			draft = fl.draft
		} else {
			draft = a.ask(fmt.Sprintf("Repo %s: open PRs/MRs as drafts?", repo), map[bool]string{true: "yes", false: "no"}[draft], []string{"yes", "no"}) == "yes"
		}
		if draft {
			pol["draft"] = true
		}
		reposDoc[repo] = pol

		// One forge entry per host; reuse what's configured (globally, say).
		host := f.target.Host
		if _, done := forges["project"][host]; done {
			continue
		}
		if _, done := forges["global"][host]; done {
			continue
		}
		existing, have := cur.Forges[host]
		kind := existing.Kind
		if kind == "" {
			kind = forge.GuessKind(host)
		}
		if kind == "" {
			kind = a.ask(fmt.Sprintf("Which forge runs at %s?", host), "gitlab", []string{"github", "gitlab"})
		}
		api := existing.API
		if flagSet("forge-api") {
			api = fl.api
		}
		if api == "" {
			api = forge.DefaultAPI(kind, host)
			if forge.GuessKind(host) == "" || (kind == "github" && host != "github.com") {
				api = a.ask(fmt.Sprintf("API base URL for %s", host), api, nil)
			}
		}
		fe := project.Forge{Kind: kind, API: api, TokenCmd: existing.TokenCmd, TokenEnv: existing.TokenEnv}
		if flagSet("forge-token-cmd") {
			fe.TokenCmd, fe.TokenEnv = strings.Fields(fl.tokenCmd), ""
		}
		if flagSet("forge-token-env") {
			fe.TokenEnv = fl.tokenEnv
			if !flagSet("forge-token-cmd") {
				fe.TokenCmd = nil
			}
		}
		if len(fe.TokenCmd) == 0 && fe.TokenEnv == "" {
			if a.tty {
				fmt.Printf("  foreman needs an API token for %s (create a PR/MR, read its state). Give a command that prints it,\n", host)
				fmt.Println("  e.g. `gh auth token`, `pass show forge/token`, `secret-tool lookup service forge`. A command works for")
				fmt.Println("  watchers started by herdr, which don't see your shell's environment variables.")
			}
			defCmd := ""
			if _, err := exec.LookPath("gh"); err == nil && kind == "github" {
				defCmd = "gh auth token"
			}
			if c := a.ask("Token command for "+host, defCmd, nil); c != "" {
				fe.TokenCmd = strings.Fields(c)
			} else {
				fe.TokenEnv = a.ask("Environment variable with the token instead", map[string]string{"github": "GITHUB_TOKEN", "gitlab": "GITLAB_TOKEN"}[kind], nil)
			}
		}
		// Check it now, so a bad token shows up here, not at the first Land.
		if who, err := checkForge(ctx, fe, f.target.Path); err != nil {
			fmt.Printf("  ✖ %s: %v\n", host, err)
		} else {
			fmt.Printf("  ✔ authenticated as %s on %s\n", who, host)
		}
		if have && fe.Kind == existing.Kind && fe.API == existing.API && strings.Join(fe.TokenCmd, " ") == strings.Join(existing.TokenCmd, " ") && fe.TokenEnv == existing.TokenEnv {
			continue // unchanged: leave it wherever it already lives
		}
		where := pick("forge-save", fl.save, fmt.Sprintf("Save the %s settings for this project only, or globally (every project)?", host), "project", []string{"project", "global"})
		if forges[where] == nil {
			forges[where] = map[string]any{}
		}
		entry := map[string]any{"kind": fe.Kind, "api": fe.API}
		if len(fe.TokenCmd) > 0 {
			entry["token_cmd"] = fe.TokenCmd
		}
		if fe.TokenEnv != "" {
			entry["token_env"] = fe.TokenEnv
		}
		forges[where][host] = entry
	}
	if len(reposDoc) == 0 {
		fmt.Println("  no repo lands, so landing stays off")
		return map[string]any{"enabled": false}, nil, nil
	}
	return map[string]any{"enabled": true, "repos": reposDoc}, forges, nil
}

// checkForge resolves a forge's token and asks the API who it belongs to. Tests replace it.
var checkForge = func(ctx context.Context, fe project.Forge, proj string) (string, error) {
	tok, err := forge.Token(ctx, fe.TokenCmd, fe.TokenEnv)
	if err != nil {
		return "", err
	}
	fc, err := forge.New(forge.Config{Kind: fe.Kind, API: fe.API, Project: proj, Token: tok})
	if err != nil {
		return "", err
	}
	who, err := fc.Whoami(ctx)
	if err != nil {
		return "", fmt.Errorf("the token didn't work: %w", err)
	}
	return who, nil
}

// saveForges writes forge entries: project ones into doc (init) or the project config,
// global ones into the global config.
func saveForges(p *project.Project, forges map[string]map[string]any) error {
	for host, entry := range forges["global"] {
		if err := project.SetGlobal([]string{"forges", host}, entry); err != nil {
			return err
		}
		fmt.Printf("  saved %s in %s\n", host, project.GlobalConfigPath())
	}
	if p != nil {
		for host, entry := range forges["project"] {
			if err := p.SetLocalKeys([]string{"forges", host}, entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func landingCmd() *cobra.Command {
	c := &cobra.Command{Use: "landing", Short: "Landing: how finished workstreams land (optional)"}
	var fl landingFlags
	var yes bool
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Turn landing on or off and set each repo's policy (asks on a terminal; flags for scripts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			repos := p.Config.Workers.Repos
			if len(repos) == 0 {
				repos = project.DetectRepos(p.Root)
			}
			a := newAsker(yes)
			doc, forges, err := landingSetup(a, cmd.Flags().Changed, p.Root, repos, fl, p.Config)
			if err != nil {
				return err
			}
			// Replace the whole [landing] table so repos that were turned off disappear.
			if err := p.SetLocal("landing", doc); err != nil {
				return err
			}
			if err := saveForges(p, forges); err != nil {
				return err
			}
			printLanding(p)
			return nil
		},
	}
	fl.register(setup)
	setup.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask; use flags and detected defaults")

	status := &cobra.Command{
		Use: "status", Short: "Show landing settings and check forge access (read-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			printLanding(p)
			landingDoctor(p, func(good bool, msg string) {
				fmt.Println(map[bool]string{true: "✔", false: "!"}[good], msg)
			})
			return nil
		},
	}

	poll := &cobra.Command{
		Use: "poll [name]", Short: "Check open PRs/MRs now (the watcher does this every few minutes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			env := workstream.LandingEnv(e.p, e.l, e.h, nil)
			for _, m := range e.l.List(false) {
				if len(args) > 0 && m.Name != args[0] {
					continue
				}
				if m.Land == nil || m.Land.State != "in-review" {
					continue
				}
				if err := env.Poll(m); err != nil {
					fmt.Printf("%s: %v\n", m.Name, err)
					continue
				}
				fmt.Printf("%s: %s\n", m.Name, m.Land.State)
			}
			return nil
		},
	}
	c.AddCommand(setup, status, poll)
	return c
}

func printLanding(p *project.Project) {
	lc := p.Config.Landing
	if !lc.Enabled {
		fmt.Println("landing: off (workstreams close out with `ws finish` → approve)")
		return
	}
	fmt.Println("landing: on")
	var repos []string
	for r := range lc.Repos {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		pol, ok := p.Config.LandingFor(r)
		switch {
		case !ok:
			fmt.Printf("  %s: off\n", r)
		case pol.PR():
			extra := ""
			if pol.HeadOwner != "" {
				extra = fmt.Sprintf(", fork %s (push %s, base from %s)", pol.HeadOwner, pol.Remote, pol.Upstream)
			}
			fmt.Printf("  %s: PR into %s on %s/%s, branch %s%s%s\n", r, pol.Base, pol.Forge, pol.Project, pol.Branch, extra, map[bool]string{true: ", draft", false: ""}[pol.Draft])
		default:
			fmt.Printf("  %s: fast-forward %s%s\n", r, pol.Base, map[bool]string{true: ", gate: " + pol.Gate, false: ""}[pol.Gate != ""])
		}
	}
}

// landingDoctor checks forge access and push access for landing repos. Problems are
// warnings: landing is optional and never blocks anything else.
func landingDoctor(p *project.Project, ok func(bool, string)) {
	if !p.Config.Landing.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for repo := range p.Config.Landing.Repos {
		pol, on := p.Config.LandingFor(repo)
		if !on || !pol.PR() {
			continue
		}
		f, err := landing.NewForge(ctx, p, pol)
		if err != nil {
			ok(false, fmt.Sprintf("landing %s: %v", repo, err))
			continue
		}
		if who, err := f.Whoami(ctx); err != nil {
			ok(false, fmt.Sprintf("landing %s: %s token: %v", repo, pol.Forge, err))
		} else {
			ok(true, fmt.Sprintf("landing %s: %s token works (%s)", repo, pol.Forge, who))
		}
		cmd := exec.CommandContext(ctx, "git", "-C", filepath.Join(p.Root, repo), "ls-remote", "--heads", pol.Remote, pol.Base)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			ok(false, fmt.Sprintf("landing %s: can't reach remote %s non-interactively (2FA, VPN, ssh agent?): %s", repo, pol.Remote, strings.TrimSpace(string(out))))
		} else {
			ok(true, fmt.Sprintf("landing %s: remote %s reachable", repo, pol.Remote))
		}
	}
}
