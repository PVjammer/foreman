// Package forge talks to code forges (GitHub, GitLab) for landing: open a pull/merge request
// for a pushed branch and look up its state. It uses the REST APIs directly with net/http,
// so neither gh nor glab is needed. Everything else about landing (pushing, deciding when to
// land) lives in the landing package; this one only speaks HTTP.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// States of a request, normalized across forges. A squash- or rebase-merged request is
// "merged": state comes from the API, never from git ancestry.
const (
	Open   = "open"
	Merged = "merged"
	Closed = "closed"
)

// MR is a pull request (GitHub) or merge request (GitLab).
type MR struct {
	ID      string `json:"id"` // GitHub PR number, GitLab MR iid
	URL     string `json:"url"`
	State   string `json:"state"`
	HeadSHA string `json:"head_sha,omitempty"`
	Title   string `json:"title,omitempty"`
}

// OpenReq opens a request from Head to Base. HeadOwner is set when the branch lives in a
// fork (GitHub: "owner:branch"); empty means the branch is in the target repo.
type OpenReq struct {
	Head, HeadOwner, Base, Title, Body string
	Draft                              bool
}

type Forge interface {
	Kind() string
	// Whoami checks the token and returns the account it belongs to.
	Whoami(ctx context.Context) (string, error)
	// Find returns the newest request whose source is head (in any state), or nil.
	Find(ctx context.Context, head, headOwner string) (*MR, error)
	Open(ctx context.Context, r OpenReq) (*MR, error)
}

// Config selects and configures a forge. Project is where requests are opened
// ("owner/repo" on GitHub, "group/sub/repo" on GitLab).
type Config struct {
	Kind    string // github | gitlab
	API     string // API base; defaults: https://api.github.com, https://<host> for gitlab
	Project string
	Token   string
	Client  *http.Client
}

func New(c Config) (Forge, error) {
	if c.Project == "" {
		return nil, errors.New("forge: no project (owner/repo) configured")
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 30 * time.Second}
	}
	switch c.Kind {
	case "github":
		if c.API == "" {
			c.API = "https://api.github.com"
		}
		return &github{c: c}, nil
	case "gitlab":
		if c.API == "" {
			return nil, errors.New("forge: gitlab needs an API URL (e.g. https://gitlab.example.com)")
		}
		return &gitlab{c: c}, nil
	}
	return nil, fmt.Errorf("forge: unknown kind %q (github | gitlab)", c.Kind)
}

// Token resolves a token from a command (preferred: works for watchers started by herdr,
// which don't inherit your shell's environment) or an environment variable. Both come from
// foreman config only, never from files a worker can write.
func Token(ctx context.Context, cmd []string, env string) (string, error) {
	if len(cmd) > 0 {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
		var stderr bytes.Buffer
		c.Stderr = &stderr
		out, err := c.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return "", fmt.Errorf("token command %q failed: %v %s", cmd[0], err, msg)
		}
		tok := strings.TrimSpace(string(out))
		if tok == "" || strings.ContainsAny(tok, " \n\t") {
			return "", fmt.Errorf("token command %q printed no single token", cmd[0])
		}
		return tok, nil
	}
	if env != "" {
		if tok := strings.TrimSpace(os.Getenv(env)); tok != "" {
			return tok, nil
		}
		return "", fmt.Errorf("no token: $%s is empty in this process (watchers started by herdr don't see your shell's variables; prefer token_cmd)", env)
	}
	return "", errors.New("no token configured (token_cmd or token_env)")
}

// APIError is a non-2xx response. Its message never includes the token.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return fmt.Sprintf("forge API %d: %s", e.Status, e.Msg) }

func do(ctx context.Context, cl *http.Client, method, url string, hdr map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "foreman-landing")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, redact(url), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Msg: apiMessage(b)}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("decode %s: %w", redact(url), err)
		}
	}
	return nil
}

// apiMessage pulls a short message out of an error body (both forges use "message").
func apiMessage(b []byte) string {
	var v struct {
		Message any `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	msg := ""
	if json.Unmarshal(b, &v) == nil {
		switch m := v.Message.(type) {
		case string:
			msg = m
		case nil:
		default:
			mb, _ := json.Marshal(m)
			msg = string(mb)
		}
		for _, e := range v.Errors {
			if e.Message != "" {
				msg += "; " + e.Message
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(b))
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func redact(u string) string {
	if i := strings.Index(u, "?"); i >= 0 {
		return u[:i]
	}
	return u
}
