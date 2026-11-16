package forge

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type github struct{ c Config }

func (g *github) Kind() string { return "github" }

func (g *github) hdr() map[string]string {
	return map[string]string{
		"Authorization":        "Bearer " + g.c.Token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
}

func (g *github) api(path string) string { return strings.TrimRight(g.c.API, "/") + path }

func (g *github) owner() string {
	o, _, _ := strings.Cut(g.c.Project, "/")
	return o
}

func (g *github) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := do(ctx, g.c.Client, "GET", g.api("/user"), g.hdr(), nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

type ghPull struct {
	Number   int     `json:"number"`
	HTMLURL  string  `json:"html_url"`
	State    string  `json:"state"` // open | closed
	MergedAt *string `json:"merged_at"`
	Title    string  `json:"title"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (p ghPull) mr() *MR {
	st := Open
	switch {
	case p.MergedAt != nil && *p.MergedAt != "":
		st = Merged
	case p.State == "closed":
		st = Closed
	}
	return &MR{ID: strconv.Itoa(p.Number), URL: p.HTMLURL, State: st, HeadSHA: p.Head.SHA, Title: p.Title}
}

func (g *github) Find(ctx context.Context, head, headOwner string) (*MR, error) {
	if headOwner == "" {
		headOwner = g.owner()
	}
	q := url.Values{"head": {headOwner + ":" + head}, "state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"10"}}
	var pulls []ghPull
	if err := do(ctx, g.c.Client, "GET", g.api("/repos/"+g.c.Project+"/pulls?"+q.Encode()), g.hdr(), nil, &pulls); err != nil {
		return nil, err
	}
	if len(pulls) == 0 {
		return nil, nil
	}
	return pulls[0].mr(), nil
}

func (g *github) Open(ctx context.Context, r OpenReq) (*MR, error) {
	head := r.Head
	if r.HeadOwner != "" {
		head = r.HeadOwner + ":" + r.Head
	}
	body := map[string]any{"title": r.Title, "head": head, "base": r.Base, "body": r.Body, "draft": r.Draft}
	if r.HeadOwner != "" && r.HeadOwner != g.owner() {
		body["maintainer_can_modify"] = true
	}
	var p ghPull
	err := do(ctx, g.c.Client, "POST", g.api("/repos/"+g.c.Project+"/pulls"), g.hdr(), body, &p)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 422 && strings.Contains(strings.ToLower(ae.Msg), "already exists") {
		if mr, ferr := g.Find(ctx, r.Head, r.HeadOwner); ferr == nil && mr != nil {
			return mr, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return p.mr(), nil
}
