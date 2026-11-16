// Package fake is an in-memory forge speaking the small subset of the GitHub and GitLab REST
// APIs that landing uses, plus a control endpoint for tests and the scenario script:
//
//	POST /_fake/set?id=<n>&state=open|merged|closed   flip a request's state
//	GET  /_fake/requests                              everything opened so far (JSON)
//
// GitHub routes live at the root (API base = server URL); GitLab routes under /api/v4
// (API base = server URL too). Both require the configured token.
package fake

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Request struct {
	ID        int    `json:"id"`
	Forge     string `json:"forge"`
	Project   string `json:"project"`
	Head      string `json:"head"`
	HeadOwner string `json:"head_owner,omitempty"`
	Base      string `json:"base"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Draft     bool   `json:"draft,omitempty"`
	State     string `json:"state"` // open | merged | closed
}

type Server struct {
	Token string
	User  string

	mu   sync.Mutex
	next int
	reqs []*Request
	// Calls counts API calls by "METHOD path" for tests.
	Calls map[string]int
}

func New(token, user string) *Server {
	return &Server{Token: token, User: user, next: 1, Calls: map[string]int{}}
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.reqs))
	for i, r := range s.reqs {
		out[i] = *r
	}
	return out
}

func (s *Server) Set(id int, state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reqs {
		if r.ID == id {
			r.State = state
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	s.mu.Lock()
	s.Calls[r.Method+" "+path]++
	s.mu.Unlock()

	if strings.HasPrefix(path, "/_fake/") {
		s.control(w, r, path)
		return
	}
	if strings.HasPrefix(path, "/api/v4/") {
		if r.Header.Get("PRIVATE-TOKEN") != s.Token {
			writeJSON(w, 401, map[string]any{"message": "401 Unauthorized"})
			return
		}
		s.gitlab(w, r, strings.TrimPrefix(path, "/api/v4"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeJSON(w, 401, map[string]any{"message": "Bad credentials"})
		return
	}
	s.github(w, r, path)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case path == "/_fake/requests":
		writeJSON(w, 200, s.Requests())
	case path == "/_fake/set" && r.Method == "POST":
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if !s.Set(id, r.URL.Query().Get("state")) {
			writeJSON(w, 404, map[string]any{"message": "no such request"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeJSON(w, 404, map[string]any{"message": "not found"})
	}
}

func (s *Server) add(rq *Request) (*Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.reqs {
		if x.Forge == rq.Forge && x.Project == rq.Project && x.Head == rq.Head && x.HeadOwner == rq.HeadOwner && x.State == "open" {
			return x, false
		}
	}
	rq.ID, rq.State = s.next, "open"
	s.next++
	s.reqs = append(s.reqs, rq)
	return rq, true
}

func (s *Server) find(forge, project, head, owner string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Forge == forge && r.Project == project && r.Head == head && (owner == "" || r.HeadOwner == owner) {
			out = append([]Request{r}, out...) // newest first
		}
	}
	return out
}

// --- GitHub ---

func (s *Server) ghPull(base string, r Request) map[string]any {
	st := "open"
	var merged any
	switch r.State {
	case "merged":
		st, merged = "closed", time.Now().UTC().Format(time.RFC3339)
	case "closed":
		st = "closed"
	}
	return map[string]any{"number": r.ID, "html_url": base + "/" + r.Project + "/pull/" + strconv.Itoa(r.ID),
		"state": st, "merged_at": merged, "title": r.Title, "head": map[string]any{"sha": "fake"}}
}

func (s *Server) github(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/user" {
		writeJSON(w, 200, map[string]any{"login": s.User})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "repos" || parts[3] != "pulls" {
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
		return
	}
	project := parts[1] + "/" + parts[2]
	base := "https://github.example"
	switch r.Method {
	case "GET":
		owner, head, _ := strings.Cut(r.URL.Query().Get("head"), ":")
		if owner == parts[1] {
			owner = ""
		}
		var out []map[string]any
		for _, rq := range s.find("github", project, head, "") {
			ho := rq.HeadOwner
			if ho == parts[1] {
				ho = ""
			}
			if ho == owner {
				out = append(out, s.ghPull(base, rq))
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	case "POST":
		var in struct {
			Title, Head, Base, Body string
			Draft                   bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Head == "" || in.Base == "" {
			writeJSON(w, 422, map[string]any{"message": "Validation Failed"})
			return
		}
		owner, head, ok := strings.Cut(in.Head, ":")
		if !ok {
			owner, head = "", in.Head
		} else if owner == parts[1] {
			owner = ""
		}
		rq, created := s.add(&Request{Forge: "github", Project: project, Head: head, HeadOwner: owner, Base: in.Base, Title: in.Title, Body: in.Body, Draft: in.Draft})
		if !created {
			writeJSON(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]any{{"message": "A pull request already exists for " + in.Head + "."}}})
			return
		}
		writeJSON(w, 201, s.ghPull(base, *rq))
	default:
		writeJSON(w, 405, map[string]any{"message": "method"})
	}
}

// --- GitLab ---

func (s *Server) glMR(r Request) map[string]any {
	st := map[string]string{"open": "opened", "merged": "merged", "closed": "closed"}[r.State]
	return map[string]any{"iid": r.ID, "web_url": "https://gitlab.example/" + r.Project + "/-/merge_requests/" + strconv.Itoa(r.ID),
		"state": st, "sha": "fake", "title": r.Title}
}

func (s *Server) gitlab(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/user" {
		writeJSON(w, 200, map[string]any{"username": s.User})
		return
	}
	// /projects/<url-encoded path>/merge_requests
	rest, ok := strings.CutPrefix(path, "/projects/")
	enc, tail, _ := strings.Cut(rest, "/")
	project, err := url.PathUnescape(enc)
	if !ok || tail != "merge_requests" || err != nil || !strings.Contains(project, "/") {
		writeJSON(w, 404, map[string]any{"message": "404 Project Not Found"})
		return
	}
	switch r.Method {
	case "GET":
		out := []map[string]any{}
		for _, rq := range s.find("gitlab", project, r.URL.Query().Get("source_branch"), "") {
			out = append(out, s.glMR(rq))
		}
		writeJSON(w, 200, out)
	case "POST":
		var in struct {
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			Title        string `json:"title"`
			Description  string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.SourceBranch == "" {
			writeJSON(w, 400, map[string]any{"message": "bad request"})
			return
		}
		rq, created := s.add(&Request{Forge: "gitlab", Project: project, Head: in.SourceBranch, Base: in.TargetBranch, Title: in.Title, Body: in.Description, Draft: strings.HasPrefix(in.Title, "Draft:")})
		if !created {
			writeJSON(w, 409, map[string]any{"message": []string{"Another open merge request already exists for this source branch: !" + strconv.Itoa(rq.ID)}})
			return
		}
		writeJSON(w, 201, s.glMR(*rq))
	default:
		writeJSON(w, 405, map[string]any{"message": "method"})
	}
}
