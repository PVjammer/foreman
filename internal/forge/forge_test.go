package forge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/forge"
	"github.com/PVjammer/foreman/internal/forge/fake"
)

const tok = "s3cret-token-value"

func newForge(t *testing.T, kind, api, project, token string) forge.Forge {
	t.Helper()
	f, err := forge.New(forge.Config{Kind: kind, API: api, Project: project, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGitHubOpenFindStates(t *testing.T) {
	srv := fake.New(tok, "nick")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	f := newForge(t, "github", ts.URL, "PVjammer/foreman", tok)

	if who, err := f.Whoami(ctx); err != nil || who != "nick" {
		t.Fatalf("whoami = %q, %v", who, err)
	}
	if mr, err := f.Find(ctx, "fm/x", ""); err != nil || mr != nil {
		t.Fatalf("find before open = %+v, %v", mr, err)
	}
	mr, err := f.Open(ctx, forge.OpenReq{Head: "fm/x", Base: "main", Title: "T", Body: "B"})
	if err != nil || mr.State != forge.Open || mr.ID != "1" || !strings.Contains(mr.URL, "/pull/1") {
		t.Fatalf("open = %+v, %v", mr, err)
	}
	// Opening again (Land pressed twice, or a retry after a crash) reuses the open PR.
	again, err := f.Open(ctx, forge.OpenReq{Head: "fm/x", Base: "main", Title: "T"})
	if err != nil || again.ID != "1" || len(srv.Requests()) != 1 {
		t.Fatalf("second open = %+v, %v (requests %d)", again, err, len(srv.Requests()))
	}
	// Squash merges look like any merge to the API: merged_at is set.
	srv.Set(1, "merged")
	if mr, _ := f.Find(ctx, "fm/x", ""); mr == nil || mr.State != forge.Merged {
		t.Fatalf("after merge = %+v", mr)
	}
	srv.Set(1, "closed")
	if mr, _ := f.Find(ctx, "fm/x", ""); mr == nil || mr.State != forge.Closed {
		t.Fatalf("after close = %+v", mr)
	}
	// A closed PR doesn't block opening a new one; Find returns the newest.
	if mr, err := f.Open(ctx, forge.OpenReq{Head: "fm/x", Base: "main", Title: "T2"}); err != nil || mr.ID != "2" {
		t.Fatalf("reopen = %+v, %v", mr, err)
	}
	if mr, _ := f.Find(ctx, "fm/x", ""); mr.ID != "2" || mr.State != forge.Open {
		t.Fatalf("newest = %+v", mr)
	}
}

func TestGitHubFork(t *testing.T) {
	srv := fake.New(tok, "nick")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	f := newForge(t, "github", ts.URL, "upstream-org/proj", tok)
	mr, err := f.Open(ctx, forge.OpenReq{Head: "fm/fix", HeadOwner: "nick", Base: "main", Title: "Fix"})
	if err != nil {
		t.Fatal(err)
	}
	rq := srv.Requests()[0]
	if rq.Project != "upstream-org/proj" || rq.HeadOwner != "nick" || rq.Head != "fm/fix" {
		t.Fatalf("request = %+v", rq)
	}
	// The same branch name in upstream itself is a different PR.
	if got, _ := f.Find(ctx, "fm/fix", ""); got != nil {
		t.Fatalf("upstream-branch lookup matched the fork PR: %+v", got)
	}
	if got, _ := f.Find(ctx, "fm/fix", "nick"); got == nil || got.ID != mr.ID {
		t.Fatalf("fork lookup = %+v", got)
	}
}

func TestGitLabOpenFindStatesAndPrefix(t *testing.T) {
	srv := fake.New(tok, "nick")
	mux := http.NewServeMux()
	mux.Handle("/gitlab/", http.StripPrefix("/gitlab", srv)) // self-hosted under a path prefix
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()
	f := newForge(t, "gitlab", ts.URL+"/gitlab", "group/sub/repo", tok)

	if who, err := f.Whoami(ctx); err != nil || who != "nick" {
		t.Fatalf("whoami = %q, %v", who, err)
	}
	mr, err := f.Open(ctx, forge.OpenReq{Head: "fm/x", Base: "main", Title: "T", Draft: true})
	if err != nil || mr.State != forge.Open {
		t.Fatalf("open = %+v, %v", mr, err)
	}
	if srv.Calls["POST /api/v4/projects/group%2Fsub%2Frepo/merge_requests"] != 1 {
		t.Fatalf("project path not URL-encoded: %v", srv.Calls)
	}
	if rq := srv.Requests()[0]; !rq.Draft || !strings.HasPrefix(rq.Title, "Draft: ") {
		t.Fatalf("draft not marked: %+v", rq)
	}
	// 409 "already exists" → the existing MR.
	if again, err := f.Open(ctx, forge.OpenReq{Head: "fm/x", Base: "main", Title: "T"}); err != nil || again.ID != mr.ID {
		t.Fatalf("second open = %+v, %v", again, err)
	}
	srv.Set(1, "merged")
	if got, _ := f.Find(ctx, "fm/x", ""); got.State != forge.Merged {
		t.Fatalf("after squash merge = %+v", got)
	}
	srv.Set(1, "closed")
	if got, _ := f.Find(ctx, "fm/x", ""); got.State != forge.Closed {
		t.Fatalf("after close = %+v", got)
	}
	if _, err := f.Open(ctx, forge.OpenReq{Head: "fm/y", HeadOwner: "me", Base: "main"}); err == nil {
		t.Fatal("gitlab fork MR should be refused (not built)")
	}
}

func TestBadTokenErrorHidesToken(t *testing.T) {
	ts := httptest.NewServer(fake.New(tok, "nick"))
	defer ts.Close()
	for _, kind := range []string{"github", "gitlab"} {
		f := newForge(t, kind, ts.URL, "o/r", "wrong-"+tok)
		_, err := f.Whoami(context.Background())
		if err == nil {
			t.Fatalf("%s: bad token accepted", kind)
		}
		if strings.Contains(err.Error(), tok) {
			t.Fatalf("%s: error leaks the token: %v", kind, err)
		}
		if ae, ok := err.(*forge.APIError); !ok || ae.Status != 401 {
			t.Fatalf("%s: want 401 APIError, got %v", kind, err)
		}
	}
}

func TestToken(t *testing.T) {
	ctx := context.Background()
	if got, err := forge.Token(ctx, []string{"echo", "  abc123 "}, ""); err != nil || got != "abc123" {
		t.Fatalf("cmd token = %q, %v", got, err)
	}
	if _, err := forge.Token(ctx, []string{"false"}, ""); err == nil {
		t.Fatal("failing command accepted")
	}
	if _, err := forge.Token(ctx, []string{"printf", ""}, ""); err == nil {
		t.Fatal("empty output accepted")
	}
	t.Setenv("FM_TEST_TOKEN", "envtok")
	if got, err := forge.Token(ctx, nil, "FM_TEST_TOKEN"); err != nil || got != "envtok" {
		t.Fatalf("env token = %q, %v", got, err)
	}
	if _, err := forge.Token(ctx, nil, "FM_TEST_TOKEN_UNSET"); err == nil || !strings.Contains(err.Error(), "token_cmd") {
		t.Fatalf("unset env: %v", err)
	}
	if _, err := forge.Token(ctx, nil, ""); err == nil {
		t.Fatal("no source accepted")
	}
}

func TestParseRemote(t *testing.T) {
	cases := map[string]forge.Remote{
		"git@github.com:PVjammer/foreman.git":                 {Host: "github.com", Path: "PVjammer/foreman"},
		"https://github.com/PVjammer/foreman":                 {Host: "github.com", Path: "PVjammer/foreman"},
		"https://github.com/PVjammer/foreman.git/":            {Host: "github.com", Path: "PVjammer/foreman"},
		"ssh://git@gitlab.corp.example:2222/grp/sub/repo.git": {Host: "gitlab.corp.example", Path: "grp/sub/repo"},
		"https://user@code.example.org/a/b.git":               {Host: "code.example.org", Path: "a/b"},
		"github.com:o/r":                                      {Host: "github.com", Path: "o/r"},
	}
	for in, want := range cases {
		got, err := forge.ParseRemote(in)
		if err != nil || got != want {
			t.Errorf("%s → %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/srv/git/repo.git", "../bare.git", "file:///tmp/x.git", "git@github.com:onlyrepo"} {
		if _, err := forge.ParseRemote(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if forge.GuessKind("github.com") != "github" || forge.GuessKind("gitlab.corp.example") != "gitlab" || forge.GuessKind("code.example.org") != "" {
		t.Error("GuessKind")
	}
}
