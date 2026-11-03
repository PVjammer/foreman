package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLayeringGlobalThenProjectWithProfileMerge(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	write(t, filepath.Join(cfgHome, "foreman", "config.toml"), `
[ui]
mode = "relay"
[workers]
profile = "pi-small"
[profiles.pi-small]
harness = "pi"
args = ["--model", "qwen"]
use_for = "little things"
`)
	root := t.TempDir()
	write(t, filepath.Join(root, Dir, "config.toml"), `
name = "proj"
[ui]
layout = "panes"
[profiles.claude]
harness = "claude"
args = ["--model", "opus"]
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Config
	if c.UI.Mode != "relay" || c.UI.Layout != "panes" {
		t.Errorf("ui = %+v, want mode from global and layout from project", c.UI)
	}
	if c.WorkerProfile() != "pi-small" {
		t.Errorf("worker profile = %q", c.WorkerProfile())
	}
	if _, pr := c.Resolve("pi-small"); pr.Harness != "pi" || len(pr.Args) != 2 {
		t.Errorf("pi-small = %+v (global profile lost)", pr)
	}
	if _, pr := c.Resolve("claude"); strings.Join(pr.Args, " ") != "--model opus" {
		t.Errorf("claude = %+v (project override lost)", pr)
	}
	if _, pr := c.Resolve("opencode"); pr.Harness != "opencode" {
		t.Errorf("built-in profile lost: %+v", pr)
	}
	if _, pr := c.Resolve("codex"); pr.Harness != "codex" {
		t.Errorf("bare harness name should resolve to itself: %+v", pr)
	}
	if c.Herdr.Session != "default" || !c.StopOnExit() {
		t.Errorf("defaults lost: session %q stop %v", c.Herdr.Session, c.StopOnExit())
	}
}

func TestLegacyConfigStillLoads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, Dir, "config.toml"), `
name = 'AI-Projects'
session = 'fm-ai-projects'
[orchestrator]
harness = 'pi'
name = 'orch'
args = []
[workers]
default_harness = 'opencode'
[ui]
stop_on_exit = false
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if n, pr := p.Config.OrchestratorProfile(); n != "pi" || pr.Harness != "pi" {
		t.Errorf("orchestrator = %s %+v", n, pr)
	}
	if p.Config.WorkerProfile() != "opencode" {
		t.Errorf("worker = %q", p.Config.WorkerProfile())
	}
	if p.Config.StopOnExit() {
		t.Errorf("stop_on_exit = false was ignored")
	}
	if p.Config.Herdr.Session != "default" {
		t.Errorf("legacy per-project session should be ignored, got %q", p.Config.Herdr.Session)
	}
}

func TestSetLocalOnlyTouchesProjectFile(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	write(t, filepath.Join(cfgHome, "foreman", "config.toml"), "[tracker]\nkind = \"jira\"\n")
	root := t.TempDir()
	write(t, filepath.Join(root, Dir, "config.toml"), "name = \"x\"\n")
	p, _ := Load(root)
	if err := p.SetLocal("ui.mode", "notifier"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, Dir, "config.toml"))
	if strings.Contains(string(b), "jira") {
		t.Errorf("global value leaked into project file:\n%s", b)
	}
	if p.Config.UI.Mode != "notifier" || p.Config.Tracker.Kind != "jira" {
		t.Errorf("after SetLocal: mode %q tracker %q", p.Config.UI.Mode, p.Config.Tracker.Kind)
	}
}

func TestDocsDirModes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	p := &Project{Root: "/work/proj", Config: Defaults()}
	p.Config.Name = "My Proj"
	if got := p.DocsDir(); got != "/work/proj/foreman-docs" {
		t.Errorf("commit: %s", got)
	}
	p.Config.Docs.Mode = "external"
	if got := p.DocsDir(); got != "/data/foreman/projects/my-proj/foreman-docs" {
		t.Errorf("external: %s", got)
	}
}

func TestExcludeAppendsOnce(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755)
	Exclude(root, "/.foreman/", "/foreman-docs/")
	Exclude(root, "/.foreman/")
	b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if strings.Count(string(b), "/.foreman/") != 1 || !strings.Contains(string(b), "/foreman-docs/") {
		t.Errorf("exclude = %q", b)
	}
}

func TestWrappedProfileParsing(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	root := t.TempDir()
	write(t, filepath.Join(root, Dir, "config.toml"), `
[profiles.pi-boxed]
harness = "pi"
args = ["--model", "qwen"]
wrapper = ["agentbox", "--level", "1"]
use_for = "security code"
[profiles.custom]
harness = "claude"
wrapper = ["/opt/box"]
wrapper_check = ["sh", "-c", "test -n \"$IN_BOX\""]
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	_, pr := p.Config.Resolve("pi-boxed")
	if !pr.Wrapped() || strings.Join(pr.Wrapper, " ") != "agentbox --level 1" || strings.Join(pr.Args, " ") != "--model qwen" {
		t.Fatalf("pi-boxed: %+v", pr)
	}
	if got := strings.Join(pr.CheckCommand(), " "); got != "test -e /run/agentbox/session" {
		t.Errorf("agentbox default check: %q", got)
	}
	_, cu := p.Config.Resolve("custom")
	if got := cu.CheckCommand(); len(got) != 3 || got[0] != "sh" {
		t.Errorf("declared check: %q", got)
	}
	// Built-in profiles stay unwrapped, exactly as before.
	if _, pi := p.Config.Resolve("pi"); pi.Wrapped() || pi.CheckCommand() != nil || pi.Validate() != nil {
		t.Errorf("built-in pi changed: %+v", pi)
	}
	if err := (Profile{Harness: "pi", Wrapper: []string{""}}).Validate(); err == nil {
		t.Error("empty wrapper command should be rejected")
	}
	if err := (Profile{Harness: "pi", WrapperCheck: []string{"true"}}).Validate(); err == nil {
		t.Error("wrapper_check without wrapper should be rejected")
	}
	if (Profile{Wrapper: []string{"/usr/local/bin/other"}}).CheckCommand() != nil {
		t.Error("an unknown wrapper declares no check")
	}
}
