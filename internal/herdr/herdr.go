// Package herdr is a thin wrapper over the herdr CLI. Every call targets one named session;
// foreman shares one session across projects, and each project is a workspace in it.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Client struct {
	Session string
	Bin     string
}

func New(session string) *Client {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	return &Client{Session: session, Bin: bin}
}

type Agent struct {
	Name           string `json:"name"`
	Kind           string `json:"agent"`
	Status         string `json:"agent_status"`
	PaneID         string `json:"pane_id"`
	WorkspaceID    string `json:"workspace_id"`
	Cwd            string `json:"cwd"`
	Focused        bool   `json:"focused"`
	StateChangeSeq uint64 `json:"state_change_seq"`
	CompletionSeq  uint64 `json:"completion_seq"`
}

type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

// SocketPath is where a session's server listens: herdr's own "default" session at
// $XDG_CONFIG_HOME/herdr/herdr.sock, named sessions under sessions/<name>/.
func SocketPath(session string) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	if session == "" || session == "default" {
		return filepath.Join(dir, "herdr", "herdr.sock")
	}
	return filepath.Join(dir, "herdr", "sessions", session, "herdr.sock")
}

// CurrentSession is the session this process runs inside, or "" outside herdr. herdr leaves
// HERDR_SESSION empty in its default session (and in popups and plugin commands), so the
// socket path decides when it is missing.
func CurrentSession() string {
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		return s
	}
	sock := os.Getenv("HERDR_SOCKET_PATH")
	if sock == "" {
		if os.Getenv("HERDR_ENV") == "1" {
			return "default"
		}
		return ""
	}
	if sock == SocketPath("default") {
		return "default"
	}
	// .../herdr/sessions/<name>/herdr.sock
	return filepath.Base(filepath.Dir(sock))
}

func (c *Client) cmd(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, c.Bin, append([]string{"--session", c.Session}, args...)...)
}

// Run returns stdout. Herdr reports server errors as JSON on stderr, which we pass through.
func (c *Client) Run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := c.cmd(ctx, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("herdr %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()+" "+err.Error()))
	}
	return out.Bytes(), nil
}

func (c *Client) call(dst any, args ...string) error {
	b, err := c.Run(args...)
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("herdr %s: bad json: %w", args[0], err)
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(env.Result, dst)
}

func (c *Client) Agents() ([]Agent, error) {
	var r struct{ Agents []Agent }
	err := c.call(&r, "agent", "list")
	return r.Agents, err
}

func (c *Client) Workspaces() ([]Workspace, error) {
	var r struct{ Workspaces []Workspace }
	err := c.call(&r, "workspace", "list")
	return r.Workspaces, err
}

type Pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	TerminalID  string `json:"terminal_id"`
	AgentStatus string `json:"agent_status"`
	Cwd         string `json:"cwd"`
}

type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
}

func (c *Client) Panes(workspaceID string) ([]Pane, error) {
	var r struct{ Panes []Pane }
	err := c.call(&r, "pane", "list", "--workspace", workspaceID)
	return r.Panes, err
}

func (c *Client) Tabs(workspaceID string) ([]Tab, error) {
	var r struct{ Tabs []Tab }
	err := c.call(&r, "tab", "list", "--workspace", workspaceID)
	return r.Tabs, err
}

// WorkspaceByLabel finds a workspace by its label ("" if none).
func (c *Client) WorkspaceByLabel(label string) (Workspace, error) {
	wss, err := c.Workspaces()
	if err != nil {
		return Workspace{}, err
	}
	for _, ws := range wss {
		if ws.Label == label {
			return ws, nil
		}
	}
	return Workspace{}, nil
}

func focusFlag(focus bool) string {
	if focus {
		return "--focus"
	}
	return "--no-focus"
}

// CreateTab returns the new tab id and its root pane id.
func (c *Client) CreateTab(workspaceID, cwd, label string, focus bool) (string, string, error) {
	var r struct {
		Tab      Tab
		RootPane Pane `json:"root_pane"`
	}
	err := c.call(&r, "tab", "create", "--workspace", workspaceID, "--cwd", cwd, "--label", label, focusFlag(focus))
	return r.Tab.ID, r.RootPane.PaneID, err
}

func (c *Client) RenameTab(id, label string) error { return c.call(nil, "tab", "rename", id, label) }
func (c *Client) FocusTab(id string) error         { return c.call(nil, "tab", "focus", id) }

// Split splits pane and returns the new pane id.
func (c *Client) Split(pane, direction, cwd string) (string, error) {
	var r struct{ Pane Pane }
	err := c.call(&r, "pane", "split", pane, "--direction", direction, "--cwd", cwd, "--no-focus")
	return r.Pane.PaneID, err
}

// RunIn types a command line into a pane's shell and presses Enter.
func (c *Client) RunIn(pane, command string) error {
	_, err := c.Run("pane", "run", pane, command) // prints nothing on success
	return err
}

// Argv is the command line for running herdr against this session (for exec-ing attach).
func (c *Client) Argv(args ...string) []string {
	return append([]string{c.Bin, "--session", c.Session}, args...)
}

func (c *Client) ClosePane(id string) error { return c.call(nil, "pane", "close", id) }
func (c *Client) Zoom(pane string) error    { return c.call(nil, "pane", "zoom", pane, "--on") }

// CreateWorkspace returns the workspace id and its root pane id.
func (c *Client) CreateWorkspace(cwd, label string, focus bool) (string, string, error) {
	f := focusFlag(focus)
	var r struct {
		Workspace Workspace
		RootPane  struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	err := c.call(&r, "workspace", "create", "--cwd", cwd, "--label", label, f)
	return r.Workspace.ID, r.RootPane.PaneID, err
}

func (c *Client) CloseWorkspace(id string) error { return c.call(nil, "workspace", "close", id) }
func (c *Client) FocusWorkspace(id string) error { return c.call(nil, "workspace", "focus", id) }

func (c *Client) StartAgent(name, kind, pane string, args []string) error {
	a := []string{"agent", "start", name, "--kind", kind, "--pane", pane, "--timeout", "90000"}
	if len(args) > 0 {
		a = append(append(a, "--"), args...)
	}
	return c.call(nil, a...)
}

// ProcessInfo is what runs in a pane's terminal: its shell and the foreground process group.
type ProcessInfo struct {
	ShellPID       int `json:"shell_pid"`
	ForegroundPGID int `json:"foreground_process_group_id"`
	Foreground     []struct {
		Name string   `json:"name"`
		Argv []string `json:"argv"`
		PID  int      `json:"pid"`
	} `json:"foreground_processes"`
}

// AtPrompt reports whether the pane's shell is in the foreground, i.e. nothing else is running.
func (pi ProcessInfo) AtPrompt() bool {
	return pi.ShellPID != 0 && (pi.ForegroundPGID == 0 || pi.ForegroundPGID == pi.ShellPID)
}

func (c *Client) ProcessInfo(pane string) (ProcessInfo, error) {
	var r struct {
		Info ProcessInfo `json:"process_info"`
	}
	err := c.call(&r, "pane", "process-info", "--pane", pane)
	return r.Info, err
}

// PaneKeys sends key presses to a pane whether or not it holds an agent.
func (c *Client) PaneKeys(pane string, keys ...string) error {
	return c.call(nil, append([]string{"pane", "send-keys", pane}, keys...)...)
}

// PaneRead returns a pane's recent output, agent or not.
func (c *Client) PaneRead(pane string, lines int) (string, error) {
	b, err := c.Run("pane", "read", pane, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
	return string(b), err
}

func (c *Client) Prompt(target, text string) error {
	return c.call(nil, "agent", "prompt", target, text)
}

func (c *Client) SendKeys(target string, keys ...string) error {
	return c.call(nil, append([]string{"agent", "send-keys", target}, keys...)...)
}

func (c *Client) Rename(target, name string) error {
	return c.call(nil, "agent", "rename", target, name)
}

func (c *Client) FocusAgent(target string) error { return c.call(nil, "agent", "focus", target) }

func (c *Client) Read(target string, lines int) (string, error) {
	b, err := c.Run("agent", "read", target, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
	return string(b), err
}

// PaneToken sets a display-only $token on a pane; the sidebar shows it via [ui.sidebar.agents] rows.
func (c *Client) PaneToken(pane, key, value string) error {
	return c.call(nil, "pane", "report-metadata", pane, "--source", "foreman", "--token", key+"="+value)
}

func (c *Client) Notify(title, body, sound string) error {
	a := []string{"notification", "show", title, "--sound", sound}
	if body != "" {
		a = append(a, "--body", body)
	}
	return c.call(nil, a...)
}

func (c *Client) ServerRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Bin, "session", "list").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == c.Session {
			return f[1] == "running"
		}
	}
	return false
}

// StartServer launches a detached headless server for the session and waits until it answers.
func (c *Client) StartServer(logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(c.Bin, "--session", c.Session, "server")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 50; i++ {
		if _, err := c.Workspaces(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("herdr server for %s did not come up (see %s)", c.Session, logPath)
}

func (c *Client) StopServer() error { return c.call(nil, "server", "stop") }

// Attach runs the herdr TUI in the foreground until the user quits or detaches.
func (c *Client) Attach() error {
	cmd := exec.Command(c.Bin, "--session", c.Session)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
