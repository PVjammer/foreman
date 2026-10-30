package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/project"
)

// mergeClaudeSettings lets Claude Code sessions (an orchestrator or worker running in herdr)
// use foreman: `foreman` runs outside Claude's sandbox and without permission prompts, and
// the sandbox may reach herdr's socket. Everything else in the settings is kept.
func mergeClaudeSettings(cur []byte, socket string) ([]byte, []string, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(cur)) > 0 {
		if err := json.Unmarshal(cur, &doc); err != nil {
			return nil, nil, fmt.Errorf("parse Claude settings: %w", err)
		}
	}
	obj := func(parent map[string]any, key string) map[string]any {
		if m, ok := parent[key].(map[string]any); ok {
			return m
		}
		m := map[string]any{}
		parent[key] = m
		return m
	}
	var added []string
	addTo := func(parent map[string]any, key, val, label string) {
		list, _ := parent[key].([]any)
		for _, v := range list {
			if v == val {
				return
			}
		}
		parent[key] = append(list, val)
		added = append(added, label)
	}
	sandbox := obj(doc, "sandbox")
	addTo(sandbox, "excludedCommands", "foreman", "sandbox.excludedCommands += foreman")
	addTo(obj(sandbox, "network"), "allowUnixSockets", socket, "sandbox.network.allowUnixSockets += "+socket)
	addTo(obj(doc, "permissions"), "allow", "Bash(foreman:*)", "permissions.allow += Bash(foreman:*)")

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep hook commands' && and <> readable
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), added, nil
}

func claudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func runSetupClaude(print bool) error {
	path := claudeSettingsPath()
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	socket := herdr.SocketPath(project.Global().Herdr.Session)
	out, added, err := mergeClaudeSettings(cur, socket)
	if err != nil {
		return err
	}
	if print {
		fmt.Print(string(out))
		return nil
	}
	if len(added) == 0 {
		fmt.Println("Claude settings already allow foreman:", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(cur) > 0 {
		_ = os.WriteFile(path+".foreman-bak", cur, 0o644)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Println("updated", path, "(backup: "+path+".foreman-bak):")
	for _, a := range added {
		fmt.Println("  ", a)
	}
	fmt.Println("   running Claude sessions pick this up when they restart")
	return nil
}

func setupClaudeCmd() *cobra.Command {
	var print bool
	c := &cobra.Command{
		Use:   "setup-claude",
		Short: "Let Claude Code agents run foreman: sandbox exclusion, herdr socket access, permission rule (~/.claude/settings.json)",
		RunE:  func(cmd *cobra.Command, args []string) error { return runSetupClaude(print) },
	}
	c.Flags().BoolVar(&print, "print", false, "print the merged settings instead of writing them")
	return c
}

// installCmd is the one-stop machine setup `make install` runs after copying the binary.
func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Set up this machine: herdr keybindings + plugin, and Claude Code settings (if Claude is installed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSetupHerdr(false, ""); err != nil {
				return fmt.Errorf("setup-herdr: %w", err)
			}
			if _, err := exec.LookPath("claude"); err == nil || exists(filepath.Dir(claudeSettingsPath())) {
				if err := runSetupClaude(false); err != nil {
					return fmt.Errorf("setup-claude: %w", err)
				}
			}
			if err := exec.Command("herdr", "server", "reload-config").Run(); err == nil {
				fmt.Println("reloaded the running herdr's config")
			}
			fmt.Println("done. In a project: `foreman init`, then `foreman up`. `foreman doctor` checks everything.")
			return nil
		},
	}
}
