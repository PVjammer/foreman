package main

import (
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/project"
)

// The user's real config at the time of writing: an old foreman block with a hand-edited
// delivery, followed by their own [ui] table.
const userConfig = `onboarding = false

# --- foreman (begin) ---
[[keys.command]]
key = "prefix+i"
type = "popup"
command = "foreman inbox"

[ui.sidebar.agents]
rows = [["state_icon", "workspace"], ["$summary"]]

[ui.toast]
delivery = "terminal"
# --- foreman (end) ---

[ui]
status_indicators = "symbols"

[theme]
name = "tokyo-night"
`

func TestMergeKeepsDeliveryAndUserSettings(t *testing.T) {
	cfg := project.Defaults()
	out, _ := mergeHerdrConfig(userConfig, "/bin/foreman", cfg)
	if !strings.Contains(out, `delivery = "terminal"`) {
		t.Errorf("user's delivery choice lost:\n%s", out)
	}
	if strings.Count(out, "\n[ui]\n") != 1 || strings.Contains(out, "tab_bar_right") {
		t.Errorf("[ui] should be the user's alone:\n%s", out)
	}
	if !strings.Contains(out, `status_indicators = "symbols"`) || !strings.Contains(out, "tokyo-night") || !strings.HasPrefix(out, "onboarding = false") {
		t.Errorf("user settings lost:\n%s", out)
	}
	if !strings.Contains(out, "/bin/foreman --active inbox") || !strings.Contains(out, `key = "prefix+shift+i"`) || strings.Contains(out, `key = "prefix+n"`) {
		t.Errorf("keys wrong (prefix+n is herdr's next_tab):\n%s", out)
	}
	again, _ := mergeHerdrConfig(out, "/bin/foreman", cfg)
	if again != out {
		t.Errorf("not idempotent:\n--- first\n%s\n--- second\n%s", out, again)
	}
}

func TestMergeRemovesOldTabBarSupervisor(t *testing.T) {
	old := userConfig + "" // the line older versions inserted into the user's [ui]
	old = strings.Replace(old, "[ui]\n", "[ui]\ntab_bar_right = [{ type = \"command\", command = \"/x/foreman status-bar\", interval_seconds = 5 }] # foreman\n", 1)
	out, _ := mergeHerdrConfig(old, "/bin/foreman", project.Defaults())
	if strings.Contains(out, "status-bar") {
		t.Errorf("old tab bar entry kept:\n%s", out)
	}
}

func TestMergeIntoEmptyConfig(t *testing.T) {
	cfg := project.Defaults()
	cfg.Keys.Inbox = "prefix+u"
	out, _ := mergeHerdrConfig("", "/bin/foreman", cfg)
	if !strings.Contains(out, `key = "prefix+u"`) || !strings.Contains(out, `delivery = "herdr"`) {
		t.Errorf("empty merge:\n%s", out)
	}
}

func TestSlugName(t *testing.T) {
	taken := map[string]bool{"fix-the-login-timeout": true}
	if got := slugName("Fix the login timeout on dev, it's flaky", func(n string) bool { return taken[n] }); got != "fix-the-login-timeout-2" {
		t.Errorf("got %q", got)
	}
	if got := slugName("2fa codes broken", func(string) bool { return false }); got != "ws-2fa-codes-broken" {
		t.Errorf("got %q", got)
	}
}
