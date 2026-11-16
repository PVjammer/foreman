package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// Remote is a git remote URL broken into the parts landing needs.
type Remote struct {
	Host string // github.com, gitlab.example.com (no port)
	Path string // owner/repo or group/sub/repo, without .git
}

// ParseRemote understands scp-style (git@host:owner/repo.git), ssh://, https:// and git://
// URLs. Local paths and file:// URLs return an error: there's no forge behind them.
func ParseRemote(raw string) (Remote, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Remote{}, fmt.Errorf("empty remote URL")
	}
	var host, path string
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return Remote{}, err
		}
		if u.Scheme == "file" || u.Host == "" {
			return Remote{}, fmt.Errorf("%s is a local remote, not a forge", s)
		}
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(s, "@"); at >= 0 && strings.Contains(s[at:], ":") {
		rest := s[at+1:]
		host, path, _ = strings.Cut(rest, ":")
	} else if h, p, ok := strings.Cut(s, ":"); ok && !strings.HasPrefix(s, "/") && !strings.Contains(h, "/") {
		host, path = h, p
	} else {
		return Remote{}, fmt.Errorf("%s is a local remote, not a forge", s)
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || !strings.Contains(path, "/") {
		return Remote{}, fmt.Errorf("can't read owner/repo from %s", s)
	}
	return Remote{Host: strings.ToLower(host), Path: path}, nil
}

// GuessKind says which forge a host probably runs: github for github.com, gitlab for hosts
// with "gitlab" in the name, "" when it can't tell (ask).
func GuessKind(host string) string {
	switch {
	case host == "github.com":
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	}
	return ""
}

// DefaultAPI is the API base for a host of a given kind.
func DefaultAPI(kind, host string) string {
	switch {
	case kind == "github" && host == "github.com":
		return "https://api.github.com"
	case kind == "github":
		return "https://" + host + "/api/v3" // GitHub Enterprise Server
	case kind == "gitlab":
		return "https://" + host
	}
	return ""
}
