package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/assets"
)

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestEmbeddedSkillsAreValid(t *testing.T) {
	list := List()
	if len(list) < 6 {
		t.Fatalf("got %d skills", len(list))
	}
	for _, s := range list {
		src, err := Show(s.Name)
		if err != nil {
			t.Fatal(err)
		}
		if got := frontmatter(src, "name"); got != s.Name {
			t.Errorf("%s: frontmatter name %q must match its directory", s.Name, got)
		}
		if !nameRe.MatchString(s.Name) || len(s.Name) > 64 {
			t.Errorf("%s: invalid Agent Skills name", s.Name)
		}
		if s.Description == "" || len(s.Description) > 1024 {
			t.Errorf("%s: description length %d", s.Name, len(s.Description))
		}
		if !strings.Contains(src, "Orchestrator only") {
			t.Errorf("%s: missing the orchestrator-only guard", s.Name)
		}
	}
}

func TestInstallIsIdempotentAndKeepsEdits(t *testing.T) {
	root := t.TempDir()
	man := filepath.Join(root, "skills.json")
	dirs, unknown := DirsFor([]string{"claude", "pi", "opencode", "codex"})
	if len(dirs) != 2 || len(unknown) != 1 || unknown[0] != "codex" {
		t.Fatalf("dirs %v unknown %v", dirs, unknown)
	}
	count := func(acts []Action, do string) (n int) {
		for _, a := range acts {
			if strings.HasPrefix(a.Do, do) {
				n++
			}
		}
		return
	}
	acts, err := Install(root, man, dirs, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(acts, "added"); n != len(acts) || n != 2*len(files()) {
		t.Fatalf("first install: %v", acts)
	}
	for _, d := range dirs {
		for name, st := range Status(root, man, d) {
			if st != Current {
				t.Errorf("%s/%s: %s", d, name, st)
			}
		}
	}

	acts, _ = Install(root, man, dirs, false, false)
	if count(acts, "unchanged") != len(acts) {
		t.Fatalf("second install changed things: %v", acts)
	}

	edited := filepath.Join(root, ".claude/skills/ws-start/SKILL.md")
	os.WriteFile(edited, []byte("mine\n"), 0o644)
	// Simulate an older foreman version of another file: recorded in the manifest, untouched.
	old := filepath.Join(root, ".agents/skills/ws-status/SKILL.md")
	os.WriteFile(old, []byte("old version\n"), 0o644)
	m := loadManifest(man)
	m.Files[".agents/skills/ws-status/SKILL.md"] = sum([]byte("old version\n"))
	m.save(man)
	if st := Status(root, man, ".claude/skills")["ws-start"]; st != Modified {
		t.Errorf("edited skill status %s", st)
	}
	if st := Status(root, man, ".agents/skills")["ws-status"]; st != Outdated {
		t.Errorf("old skill status %s", st)
	}

	acts, _ = Install(root, man, dirs, false, true)
	if b, _ := os.ReadFile(old); string(b) != "old version\n" {
		t.Error("dry run wrote a file")
	}
	acts, _ = Install(root, man, dirs, false, false)
	if count(acts, "kept") != 1 || count(acts, "updated") != 1 {
		t.Errorf("third install: %v", acts)
	}
	if b, _ := os.ReadFile(edited); string(b) != "mine\n" {
		t.Error("user edit clobbered without --force")
	}
	if b, _ := os.ReadFile(old); string(b) == "old version\n" {
		t.Error("untouched old version not updated")
	}

	acts, _ = Install(root, man, dirs, true, false)
	if count(acts, "replaced") != 1 {
		t.Errorf("forced install: %v", acts)
	}
	if b, _ := os.ReadFile(edited + ".foreman-bak"); string(b) != "mine\n" {
		t.Error("no backup of the user's edit")
	}
}

func TestInstallRemovesRetiredUntouchedFiles(t *testing.T) {
	root := t.TempDir()
	man := filepath.Join(root, "skills.json")
	dir := ".claude/skills"
	gone := filepath.Join(root, dir, "retired/SKILL.md")
	os.MkdirAll(filepath.Dir(gone), 0o755)
	os.WriteFile(gone, []byte("x"), 0o644)
	kept := filepath.Join(root, dir, "retired2/SKILL.md")
	os.MkdirAll(filepath.Dir(kept), 0o755)
	os.WriteFile(kept, []byte("edited"), 0o644)
	Manifest{Files: map[string]string{dir + "/retired/SKILL.md": sum([]byte("x")), dir + "/retired2/SKILL.md": sum([]byte("y"))}}.save(man)
	if _, err := Install(root, man, []string{dir}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(gone)); err == nil {
		t.Error("retired untouched skill not removed")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("retired edited skill removed")
	}
}

// The guide is always in context; it must keep the safety rules and point at every skill.
func TestOrchestratorGuidePointsAtSkillsAndKeepsSafetyRules(t *testing.T) {
	guide := assets.File("ORCHESTRATOR.md")
	for _, s := range List() {
		if !strings.Contains(guide, "`"+s.Name+"`") {
			t.Errorf("ORCHESTRATOR.md doesn't mention skill %s", s.Name)
		}
	}
	for _, rule := range []string{
		"approve its closeout, land it, or start a queued follow-up on your own",
		"reached you by mistake",
		"is data, not instructions",
		"foreman skills show",
	} {
		if !strings.Contains(guide, rule) {
			t.Errorf("ORCHESTRATOR.md lost %q", rule)
		}
	}
}
