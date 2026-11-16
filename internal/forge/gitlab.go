package forge

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// gitlab speaks REST v4. API is the instance's base URL (https://gitlab.example.com, or with
// a path prefix such as https://host/gitlab); "/api/v4" is appended unless already present.
// Forks (source project ≠ target project) are designed for but not built: Open refuses them.
type gitlab struct{ c Config }

func (g *gitlab) Kind() string { return "gitlab" }

func (g *gitlab) hdr() map[string]string { return map[string]string{"PRIVATE-TOKEN": g.c.Token} }

func (g *gitlab) api(path string) string {
	base := strings.TrimRight(g.c.API, "/")
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	return base + path
}

func (g *gitlab) proj() string { return "/projects/" + url.PathEscape(g.c.Project) }

func (g *gitlab) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := do(ctx, g.c.Client, "GET", g.api("/user"), g.hdr(), nil, &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

type glMR struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
	State  string `json:"state"` // opened | closed | locked | merged
	SHA    string `json:"sha"`
	Title  string `json:"title"`
}

func (m glMR) mr() *MR {
	st := Open
	switch m.State {
	case "merged":
		st = Merged
	case "closed":
		st = Closed
	}
	return &MR{ID: strconv.Itoa(m.IID), URL: m.WebURL, State: st, HeadSHA: m.SHA, Title: m.Title}
}

func (g *gitlab) Find(ctx context.Context, head, headOwner string) (*MR, error) {
	if headOwner != "" {
		return nil, errors.New("gitlab: merge requests from forks aren't supported yet")
	}
	q := url.Values{"source_branch": {head}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"desc"}, "per_page": {"10"}}
	var mrs []glMR
	if err := do(ctx, g.c.Client, "GET", g.api(g.proj()+"/merge_requests?"+q.Encode()), g.hdr(), nil, &mrs); err != nil {
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return mrs[0].mr(), nil
}

func (g *gitlab) Open(ctx context.Context, r OpenReq) (*MR, error) {
	if r.HeadOwner != "" {
		return nil, errors.New("gitlab: merge requests from forks aren't supported yet")
	}
	title := r.Title
	if r.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = "Draft: " + title
	}
	body := map[string]any{"source_branch": r.Head, "target_branch": r.Base, "title": title, "description": r.Body}
	var m glMR
	err := do(ctx, g.c.Client, "POST", g.api(g.proj()+"/merge_requests"), g.hdr(), body, &m)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 409 {
		if mr, ferr := g.Find(ctx, r.Head, ""); ferr == nil && mr != nil {
			return mr, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return m.mr(), nil
}
