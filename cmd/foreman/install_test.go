package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const claudeSettings = `{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "test -x ~/x.sh && ~/x.sh stop"}]}]
  },
  "permissions": {"allow": ["Bash(git status)"]},
  "agentPushNotifEnabled": true
}`

func TestMergeClaudeSettingsKeepsEverythingAndIsIdempotent(t *testing.T) {
	out, added, err := mergeClaudeSettings([]byte(claudeSettings), "/home/u/.config/herdr/herdr.sock")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 3 {
		t.Errorf("added = %v", added)
	}
	s := string(out)
	if !strings.Contains(s, "test -x ~/x.sh && ~/x.sh stop") {
		t.Errorf("hook command lost or escaped:\n%s", s)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	allow := doc["permissions"].(map[string]any)["allow"].([]any)
	if len(allow) != 2 || allow[0] != "Bash(git status)" || allow[1] != "Bash(foreman:*)" {
		t.Errorf("allow = %v", allow)
	}
	if doc["agentPushNotifEnabled"] != true {
		t.Errorf("other settings lost")
	}
	sb := doc["sandbox"].(map[string]any)
	if sb["excludedCommands"].([]any)[0] != "foreman" || sb["network"].(map[string]any)["allowUnixSockets"].([]any)[0] != "/home/u/.config/herdr/herdr.sock" {
		t.Errorf("sandbox = %v", sb)
	}
	again, added2, _ := mergeClaudeSettings(out, "/home/u/.config/herdr/herdr.sock")
	if len(added2) != 0 || string(again) != s {
		t.Errorf("second merge changed things: %v", added2)
	}
}

func TestMergeClaudeSettingsEmpty(t *testing.T) {
	out, added, err := mergeClaudeSettings(nil, "/s.sock")
	if err != nil || len(added) != 3 || !strings.Contains(string(out), `"Bash(foreman:*)"`) {
		t.Fatalf("err %v added %v out %s", err, added, out)
	}
}
