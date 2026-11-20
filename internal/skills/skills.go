// Package skills installs foreman's agent skills (Agent Skills format: a directory per skill
// with a SKILL.md) into a project's harness skill directories, so the orchestrator loads
// procedures on demand instead of carrying them all in ORCHESTRATOR.md.
//
// Installs are tracked in .foreman/skills.json (sha256 of each file as foreman wrote it), so
// re-installing is idempotent: identical files are left alone, files foreman wrote earlier
// and nobody touched are updated, and files the user edited are kept unless forced.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PVjammer/foreman/internal/assets"
)

// Dirs maps each supported harness to its project skill directory (relative to the root).
// pi and opencode both read .agents/skills; opencode also reads .claude/skills.
var Dirs = map[string]string{
	"claude":   ".claude/skills",
	"pi":       ".agents/skills",
	"opencode": ".agents/skills",
}

// Harnesses lists the supported harnesses in a stable order.
var Harnesses = []string{"claude", "pi", "opencode"}

const root = "skills"

// Skill is one embedded skill.
type Skill struct {
	Name, Description string
}

// List returns the embedded skills, sorted by name.
func List() []Skill {
	ents, _ := fs.ReadDir(assets.FS, root)
	var out []Skill
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(assets.FS, path.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, Skill{Name: e.Name(), Description: frontmatter(string(b), "description")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Show returns a skill's SKILL.md, for harnesses that don't load skills themselves.
func Show(name string) (string, error) {
	b, err := fs.ReadFile(assets.FS, path.Join(root, name, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("no skill %q (see `foreman skills`)", name)
	}
	return string(b), nil
}

// frontmatter reads a single-line key from a SKILL.md's YAML frontmatter.
func frontmatter(src, key string) string {
	parts := strings.SplitN(src, "---", 3)
	if len(parts) < 3 {
		return ""
	}
	for _, ln := range strings.Split(parts[1], "\n") {
		if v, ok := strings.CutPrefix(ln, key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// files returns every embedded file of every skill: path relative to the skill dir root
// ("ws-start/SKILL.md") → content.
func files() map[string][]byte {
	out := map[string][]byte{}
	_ = fs.WalkDir(assets.FS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(assets.FS, p)
		out[strings.TrimPrefix(p, root+"/")] = b
		return nil
	})
	return out
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Manifest records what foreman installed: project-relative path → sha256 as written.
type Manifest struct {
	Files map[string]string `json:"files"`
}

func loadManifest(p string) Manifest {
	m := Manifest{Files: map[string]string{}}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m
}

func (m Manifest) save(p string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// Action is what Install did (or would do) to one file.
type Action struct {
	Path string // relative to the project root
	Do   string // added | updated | unchanged | kept (edited) | replaced (forced) | removed
}

// Install writes the embedded skills into each of dirs (relative to projectRoot), tracking
// them in manifestPath. force replaces files the user edited (backing them up first).
// dryRun reports the actions without writing anything.
func Install(projectRoot, manifestPath string, dirs []string, force, dryRun bool) ([]Action, error) {
	man := loadManifest(manifestPath)
	want := files()
	var acts []Action
	for _, dir := range dirs {
		var keys []string
		for k := range want {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rel := filepath.ToSlash(filepath.Join(dir, k))
			abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
			nb := want[k]
			cur, err := os.ReadFile(abs)
			do := ""
			switch {
			case err != nil:
				do = "added"
			case string(cur) == string(nb):
				do = "unchanged"
			case man.Files[rel] == sum(cur):
				do = "updated"
			case force:
				do = "replaced (forced; old copy in " + filepath.Base(abs) + ".foreman-bak)"
			default:
				do = "kept (edited; --force to replace)"
			}
			acts = append(acts, Action{rel, do})
			if dryRun || strings.HasPrefix(do, "kept") {
				continue
			}
			if do != "unchanged" {
				if strings.HasPrefix(do, "replaced") {
					if err := os.WriteFile(abs+".foreman-bak", cur, 0o644); err != nil {
						return acts, err
					}
				}
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return acts, err
				}
				if err := os.WriteFile(abs, nb, 0o644); err != nil {
					return acts, err
				}
			}
			man.Files[rel] = sum(nb)
		}
		// Files foreman installed into this dir earlier that no longer ship: remove them if
		// untouched, keep them (and their record) if edited.
		for rel, h := range man.Files {
			if !strings.HasPrefix(rel, dir+"/") {
				continue
			}
			if _, ok := want[strings.TrimPrefix(rel, dir+"/")]; ok {
				continue
			}
			abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
			cur, err := os.ReadFile(abs)
			switch {
			case err != nil:
				if !dryRun {
					delete(man.Files, rel)
				}
			case sum(cur) == h:
				acts = append(acts, Action{rel, "removed"})
				if !dryRun {
					_ = os.Remove(abs)
					_ = os.Remove(filepath.Dir(abs)) // only succeeds if now empty
					delete(man.Files, rel)
				}
			}
		}
	}
	if dryRun {
		return acts, nil
	}
	return acts, man.save(manifestPath)
}

// State of one skill in one directory.
const (
	Current  = "installed"
	Outdated = "outdated" // foreman's earlier version, untouched: `skills install` updates it
	Modified = "edited"   // changed by the user (or not installed by foreman): kept on install
	Missing  = "missing"
)

// Status reports each skill's state in dir (relative to projectRoot).
func Status(projectRoot, manifestPath, dir string) map[string]string {
	man := loadManifest(manifestPath)
	out := map[string]string{}
	rank := map[string]int{Current: 0, Outdated: 1, Modified: 2, Missing: 3}
	for k, nb := range files() {
		name := strings.SplitN(k, "/", 2)[0]
		rel := filepath.ToSlash(filepath.Join(dir, k))
		cur, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(rel)))
		st := Current
		switch {
		case err != nil:
			st = Missing
		case string(cur) == string(nb):
		case man.Files[rel] == sum(cur):
			st = Outdated
		default:
			st = Modified
		}
		if prev, ok := out[name]; !ok || rank[st] > rank[prev] {
			out[name] = st
		}
	}
	return out
}

// DirsFor maps harness names to their skill dirs, deduplicated, in the given order. Unknown
// harnesses are returned separately.
func DirsFor(harnesses []string) (dirs, unknown []string) {
	seen := map[string]bool{}
	for _, h := range harnesses {
		d, ok := Dirs[h]
		if !ok {
			if !seen["?"+h] {
				unknown = append(unknown, h)
				seen["?"+h] = true
			}
			continue
		}
		if !seen[d] {
			dirs = append(dirs, d)
			seen[d] = true
		}
	}
	return dirs, unknown
}

// ExcludePatterns are the git-exclude patterns for foreman's skills in dirs.
func ExcludePatterns(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		for _, s := range List() {
			out = append(out, "/"+d+"/"+s.Name+"/")
		}
	}
	return out
}
