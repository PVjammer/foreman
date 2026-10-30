// Package project finds the foreman project for the current directory and loads its config.
// A project is any directory containing .foreman/config.toml.
//
// Config is layered: built-in defaults, then the global ~/.config/foreman/config.toml, then
// the project's .foreman/config.toml. Workstream types and per-workstream flags override
// on top of that (see the types and workstream packages).
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const Dir = ".foreman"

// Profile is a named way to run an agent. UseFor tells the orchestrator when to pick it.
type Profile struct {
	Harness string   `toml:"harness"`
	Args    []string `toml:"args,omitempty"`
	UseFor  string   `toml:"use_for,omitempty"`
}

type Config struct {
	Name string `toml:"name,omitempty"`

	Herdr struct {
		// One shared herdr session for every project; each project is a workspace in it.
		Session string `toml:"session,omitempty"`
	} `toml:"herdr"`

	Orchestrator struct {
		Profile string `toml:"profile,omitempty"`
		Name    string `toml:"name,omitempty"`
		// Harness/Args: an ad-hoc profile (also what older configs used).
		Harness string   `toml:"harness,omitempty"`
		Args    []string `toml:"args,omitempty"`
	} `toml:"orchestrator"`

	Workers struct {
		Profile string `toml:"profile,omitempty"`
		Type    string `toml:"type,omitempty"`
		// DefaultHarness is the pre-profile setting, still honoured when Profile is empty.
		DefaultHarness string   `toml:"default_harness,omitempty"`
		Repos          []string `toml:"repos,omitempty"`
	} `toml:"workers"`

	Profiles map[string]Profile `toml:"profiles,omitempty"`

	UI struct {
		// notifier | inbox | relay — the three UX candidates.
		Mode string `toml:"mode,omitempty"`
		// tabs: one tab per workstream. panes: all workers in one "workstreams" tab, focused
		// pane zoomed (unzoomed panes get small, so this suits a handful of workers).
		Layout     string `toml:"layout,omitempty"`
		StopOnExit *bool  `toml:"stop_on_exit,omitempty"`
	} `toml:"ui"`

	// Keys are herdr keybindings, written by `foreman setup-herdr`. The defaults avoid
	// herdr's own bindings (s, o, w, n, q, p, ... are taken).
	Keys struct {
		Inbox        string `toml:"inbox,omitempty"`        // popup: worker events
		Next         string `toml:"next,omitempty"`         // jump to the workstream that most needs you
		Orchestrator string `toml:"orchestrator,omitempty"` // switch to the orchestrator pane (again: back)
		OrchFloat    string `toml:"orchestrator_float,omitempty"`
		Scratch      string `toml:"scratch,omitempty"`       // popup: the scratch session
		ScratchClear string `toml:"scratch_clear,omitempty"` // restart scratch with a fresh conversation
		Terminal     string `toml:"terminal,omitempty"`      // popup: the project's persistent shell
		Dock         string `toml:"dock,omitempty"`          // pick a session to dock beside the orchestrator
		New          string `toml:"new,omitempty"`           // popup: ask the orchestrator for a workstream
	} `toml:"keys"`

	Scratch struct {
		// Profile for the scratch session: quick, throwaway questions with project context.
		Profile string `toml:"profile,omitempty"`
	} `toml:"scratch"`

	Summarizer struct {
		Enabled *bool  `toml:"enabled,omitempty"`
		Model   string `toml:"model,omitempty"`
		URL     string `toml:"url,omitempty"`
	} `toml:"summarizer"`

	Tracker struct {
		// ledger | jira | linear — instructions for the orchestrator live in .foreman/tracker.md.
		Kind string `toml:"kind,omitempty"`
	} `toml:"tracker"`

	Docs struct {
		// Where closeouts fold knowledge back in. Mode: commit | local | external.
		Dir  string `toml:"dir,omitempty"`
		Mode string `toml:"mode,omitempty"`
	} `toml:"docs"`

	Hygiene struct {
		// Suggest closing out or splitting a workstream past these limits.
		StaleDays int `toml:"stale_days,omitempty"`
		MaxTurns  int `toml:"max_turns,omitempty"`
		MaxOpen   int `toml:"max_open,omitempty"`
	} `toml:"hygiene"`
}

func (c *Config) StopOnExit() bool { return c.UI.StopOnExit == nil || *c.UI.StopOnExit }
func (c *Config) SummarizerEnabled() bool {
	return c.Summarizer.Enabled == nil || *c.Summarizer.Enabled
}

var DocsModes = []string{"commit", "local", "external"}

// Defaults are the built-in layer. Profiles here are starting points; edit them globally.
func Defaults() Config {
	var c Config
	c.Herdr.Session = "default" // herdr's own session, so plain `herdr` opens the same place
	c.Orchestrator.Profile = "claude"
	c.Orchestrator.Name = "orch"
	c.Workers.Profile = "claude"
	c.Workers.Type = "feature"
	c.Profiles = map[string]Profile{
		"claude":   {Harness: "claude", UseFor: "features, tricky bugs, anything cross-repo or long-running"},
		"pi":       {Harness: "pi", UseFor: "small, well-scoped tasks: lookups, mechanical edits, running tests"},
		"opencode": {Harness: "opencode", UseFor: "an alternative general-purpose worker"},
	}
	c.UI.Mode = "inbox"
	c.UI.Layout = "tabs"
	c.Keys.Inbox = "prefix+i"
	c.Keys.Next = "prefix+shift+i"
	c.Keys.Orchestrator = "prefix+m"
	c.Keys.OrchFloat = "prefix+shift+m"
	c.Keys.Scratch = "prefix+a"
	c.Keys.ScratchClear = "prefix+shift+a"
	c.Keys.Terminal = "prefix+t"
	c.Keys.Dock = "prefix+d"
	c.Keys.New = "prefix+f"
	c.Scratch.Profile = "claude"
	c.Summarizer.Model = "gemma4:latest"
	c.Summarizer.URL = "http://localhost:11434"
	c.Tracker.Kind = "ledger"
	c.Docs.Dir = "foreman-docs"
	c.Docs.Mode = "commit"
	c.Hygiene.StaleDays = 3
	c.Hygiene.MaxTurns = 40
	c.Hygiene.MaxOpen = 8
	return c
}

type Project struct {
	Root   string
	Config Config
}

func (p *Project) Path(parts ...string) string {
	return filepath.Join(append([]string{p.Root, Dir}, parts...)...)
}

// WorkstreamDir is where an open workstream lives; closed ones move to archive/.
func (p *Project) WorkstreamDir(name string) string {
	live := p.Path("ws", name)
	if _, err := os.Stat(live); err != nil {
		if arch := p.Path("archive", name); exists(arch) {
			return arch
		}
	}
	return live
}

// DocsDir is the project's docs folder, wherever its mode puts it.
func (p *Project) DocsDir() string {
	d := p.Config.Docs.Dir
	if filepath.IsAbs(d) {
		return d
	}
	if p.Config.Docs.Mode == "external" {
		return filepath.Join(DataDir(), "projects", Slug(p.Config.Name), d)
	}
	return filepath.Join(p.Root, d)
}

// Resolve turns a profile name, or a bare harness name, into a Profile.
func (c *Config) Resolve(name string) (string, Profile) {
	if pr, ok := c.Profiles[name]; ok {
		if pr.Harness == "" {
			pr.Harness = name
		}
		return name, pr
	}
	return name, Profile{Harness: name}
}

// OrchestratorProfile honours an explicit harness (older configs) over the profile name.
func (c *Config) OrchestratorProfile() (string, Profile) {
	if c.Orchestrator.Harness != "" {
		return c.Orchestrator.Harness, Profile{Harness: c.Orchestrator.Harness, Args: c.Orchestrator.Args}
	}
	return c.Resolve(c.Orchestrator.Profile)
}

func (c *Config) WorkerProfile() string {
	if c.Workers.DefaultHarness != "" && c.Workers.Profile == "claude" {
		return c.Workers.DefaultHarness
	}
	return c.Workers.Profile
}

// ProfileNames is sorted for stable output.
func (c *Config) ProfileNames() []string {
	var out []string
	for k := range c.Profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

func Slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}

func ConfigDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "foreman")
}

func DataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "foreman")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "foreman")
}

func GlobalConfigPath() string { return filepath.Join(ConfigDir(), "config.toml") }

// Find walks up from dir looking for .foreman/config.toml.
func Find(dir string) (*Project, error) {
	d, _ := filepath.Abs(dir)
	for {
		if exists(filepath.Join(d, Dir, "config.toml")) {
			return Load(d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return nil, errors.New("no foreman project here (run `foreman init` at the project root)")
}

// ByName loads a registered project by its name (the label of its herdr workspace).
func ByName(name string) (*Project, error) {
	root, ok := Registry()[name]
	if !ok {
		return nil, fmt.Errorf("no registered project %q", name)
	}
	return Load(root)
}

// Global loads defaults plus the global config, for commands that run outside a project.
func Global() Config {
	c := Defaults()
	_ = layer(&c, GlobalConfigPath())
	return c
}

func Load(root string) (*Project, error) {
	c := Defaults()
	if err := layer(&c, GlobalConfigPath()); err != nil {
		return nil, err
	}
	if err := layer(&c, filepath.Join(root, Dir, "config.toml")); err != nil {
		return nil, err
	}
	if c.Name == "" {
		c.Name = filepath.Base(root)
	}
	return &Project{Root: root, Config: c}, nil
}

// layer decodes a TOML file over c. Profiles merge by name instead of being replaced.
func layer(c *Config, path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	base := c.Profiles
	c.Profiles = nil
	if err := toml.Unmarshal(b, c); err != nil {
		c.Profiles = base
		return fmt.Errorf("parse %s: %w", path, err)
	}
	merged := map[string]Profile{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range c.Profiles {
		merged[k] = v
	}
	c.Profiles = merged
	return nil
}

// SetLocal changes one key in the project's own config file, leaving the global layer and
// the project's other settings alone. key is dotted, e.g. "ui.mode".
func (p *Project) SetLocal(key string, value any) error {
	path := p.Path("config.toml")
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, &doc); err != nil {
			return err
		}
	}
	setPath(doc, strings.Split(key, "."), value)
	b, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	np, err := Load(p.Root)
	if err == nil {
		p.Config = np.Config
	}
	return err
}

func setPath(m map[string]any, keys []string, v any) {
	if len(keys) == 1 {
		m[keys[0]] = v
		return
	}
	sub, ok := m[keys[0]].(map[string]any)
	if !ok {
		sub = map[string]any{}
		m[keys[0]] = sub
	}
	setPath(sub, keys[1:], v)
}

// WriteFile writes a fresh project config from a map of the settings chosen at init.
func WriteFile(root string, doc map[string]any) error {
	b, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, Dir, "config.toml"), b, 0o644)
}

// Registry maps project names (= herdr workspace labels) to roots: ~/.config/foreman/projects.json.
func registryPath() string { return filepath.Join(ConfigDir(), "projects.json") }

func Registry() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(registryPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func Register(name, root string) error {
	m := Registry()
	if m[name] == root {
		return nil
	}
	m[name] = root
	_ = os.MkdirAll(filepath.Dir(registryPath()), 0o755)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(registryPath(), b, 0o644)
}

// DetectRepos lists git repos under root (relative paths, "." for the root itself),
// skipping .foreman, node_modules and anything deeper than three levels.
func DetectRepos(root string) []string {
	var repos []string
	if exists(filepath.Join(root, ".git")) {
		repos = append(repos, ".")
	}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		base := d.Name()
		if base == Dir || base == "node_modules" || base == ".git" || base == "archive" || strings.HasPrefix(base, ".") {
			return filepath.SkipDir
		}
		if strings.Count(rel, string(filepath.Separator)) >= 3 {
			return filepath.SkipDir
		}
		if exists(filepath.Join(path, ".git")) {
			repos = append(repos, rel)
			return filepath.SkipDir
		}
		return nil
	})
	return repos
}

// Exclude adds patterns to the root repo's .git/info/exclude (never committed), so docs
// mode "local" keeps foreman's files out of git without touching .gitignore.
func Exclude(root string, patterns ...string) error {
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil // not a git repo (or a worktree/submodule file): nothing to exclude from
	}
	path := filepath.Join(gitDir, "info", "exclude")
	cur, _ := os.ReadFile(path)
	s := string(cur)
	var add []string
	for _, pat := range patterns {
		found := false
		for _, ln := range strings.Split(s, "\n") {
			if strings.TrimSpace(ln) == pat {
				found = true
			}
		}
		if !found {
			add = append(add, pat)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += "# foreman (docs mode: local)\n" + strings.Join(add, "\n") + "\n"
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, []byte(s), 0o644)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
