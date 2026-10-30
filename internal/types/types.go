// Package types loads workstream types: markdown files whose frontmatter sets the mechanics
// (worktrees, default profile, when to check in) and whose sections are the instructions for
// each phase (start, work, closeout). foreman only decides *when* to send each phase.
//
// Lookup order: .foreman/types/<name>.md, ~/.config/foreman/types/<name>.md, built-ins.
package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PVjammer/foreman/internal/assets"
	"github.com/PVjammer/foreman/internal/project"
)

type Type struct {
	Name        string
	Description string
	Worktrees   bool   // build in a git worktree per repo
	ReadOnly    bool   // the worker must not change code or shared systems
	Profile     string // default worker profile ("" = project default)
	CheckIn     string // free text: when the worker must stop for approval
	Phases      map[string]string
	Source      string // "project", "global" or "built-in"
	Path        string
}

// Phase returns a phase's instructions ("" if the type doesn't define it).
func (t *Type) Phase(name string) string { return t.Phases[name] }

// Aliases keep the first prototype's kinds working.
var aliases = map[string]string{"build": "feature", "investigate": "research"}

func Canonical(name string) string {
	if a, ok := aliases[name]; ok {
		return a
	}
	return name
}

func dirs(p *project.Project) []struct{ dir, source string } {
	return []struct{ dir, source string }{
		{p.Path("types"), "project"},
		{filepath.Join(project.ConfigDir(), "types"), "global"},
	}
}

// Get finds a type by name, most specific layer first.
func Get(p *project.Project, name string) (*Type, error) {
	name = Canonical(name)
	for _, d := range dirs(p) {
		path := filepath.Join(d.dir, name+".md")
		if b, err := os.ReadFile(path); err == nil {
			t, err := Parse(name, string(b))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			t.Source, t.Path = d.source, path
			return t, nil
		}
	}
	if b, err := assets.FS.ReadFile("templates/types/" + name + ".md"); err == nil {
		t, err := Parse(name, string(b))
		if err != nil {
			return nil, err
		}
		t.Source = "built-in"
		return t, nil
	}
	return nil, fmt.Errorf("no workstream type %q (see `foreman types`)", name)
}

// List returns every visible type; a project or global file shadows a built-in of the same name.
func List(p *project.Project) []*Type {
	names := map[string]bool{}
	entries, _ := fs.ReadDir(assets.FS, "templates/types")
	for _, e := range entries {
		names[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	for _, d := range dirs(p) {
		es, _ := os.ReadDir(d.dir)
		for _, e := range es {
			if strings.HasSuffix(e.Name(), ".md") {
				names[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
	}
	var out []*Type
	for n := range names {
		if t, err := Get(p, n); err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltIn returns a built-in type's source, for copying into a project to customize.
func BuiltIn(name string) (string, bool) {
	b, err := assets.FS.ReadFile("templates/types/" + Canonical(name) + ".md")
	return string(b), err == nil
}

// Parse reads "---\nkey: value\n---" frontmatter and "## phase" sections.
func Parse(name, src string) (*Type, error) {
	t := &Type{Name: name, Phases: map[string]string{}}
	body := src
	if strings.HasPrefix(src, "---") {
		end := strings.Index(src[3:], "\n---")
		if end < 0 {
			return nil, fmt.Errorf("unterminated frontmatter")
		}
		for _, ln := range strings.Split(src[3:3+end], "\n") {
			k, v, ok := strings.Cut(ln, ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
			switch k {
			case "description":
				t.Description = v
			case "worktrees":
				t.Worktrees = v == "true"
			case "read_only":
				t.ReadOnly = v == "true"
			case "profile":
				t.Profile = v
			case "check_in":
				t.CheckIn = v
			}
		}
		body = src[3+end+4:]
	}
	var cur string
	var buf []string
	flush := func() {
		if cur != "" {
			t.Phases[cur] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
	}
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "## ") {
			flush()
			cur, buf = strings.ToLower(strings.TrimSpace(ln[3:])), nil
			continue
		}
		buf = append(buf, ln)
	}
	flush()
	return t, nil
}
