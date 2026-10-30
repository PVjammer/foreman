// Package digest builds the mechanical roll-up the orchestrator turns into status updates:
// workstreams with their latest reports, events, commits and backlog for a period.
package digest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

// ParseSince accepts Go durations plus "Nd" for days.
func ParseSince(s string) (time.Time, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil {
			return time.Now().AddDate(0, 0, -n), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since: use e.g. 24h or 7d")
	}
	return time.Now().Add(-d), nil
}

func gitLog(dir, rev string, since time.Time, max int) []string {
	args := []string{"-C", dir, "log", "--since=" + since.Format(time.RFC3339), "--format=%h %ad %s", "--date=short", "-n", fmt.Sprint(max)}
	if rev != "" {
		args = append(args, rev)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

func Build(p *project.Project, since time.Time) string {
	l := ledger.New(p)
	var b strings.Builder
	fmt.Fprintf(&b, "# Digest: %s since %s\n\n", p.Config.Name, since.Format("Mon 2006-01-02 15:04"))

	events := l.Events(since)
	touched := map[string]bool{}
	for _, e := range events {
		touched[e.WS] = true
	}

	var open, closed []*ledger.Meta
	for _, m := range l.List(true) {
		switch {
		case !m.Closed:
			open = append(open, m)
		case m.ClosedAt != nil && m.ClosedAt.After(since):
			closed = append(closed, m)
		}
	}

	section := func(title string, ms []*ledger.Meta) {
		if len(ms) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
		for _, m := range ms {
			st := l.Status(m.Name)
			report, rstatus, rmod := l.Report(m.Name)
			ticket := ""
			if m.Ticket != "" {
				ticket = " · " + m.Ticket
			}
			fmt.Fprintf(&b, "### %s (%s%s)\n", m.Name, m.Kind, ticket)
			fmt.Fprintf(&b, "- Goal: %s\n", firstLine(m.Goal))
			fmt.Fprintf(&b, "- Agent: %s; last event: %s; report status: %s\n", or(st.AgentStatus, "unknown"), or(st.LastEvent, "none"), or(rstatus, "no report"))
			if st.Summary != "" {
				fmt.Fprintf(&b, "- Latest summary: %s\n", st.Summary)
			}
			if !touched[m.Name] {
				b.WriteString("- No activity in this period.\n")
			}
			if report != "" {
				fmt.Fprintf(&b, "- Report (updated %s):\n", rmod.Format("01-02 15:04"))
				for _, sec := range []string{"Summary", "Changes", "Open questions", "Follow-ups"} {
					if s := ledger.Section(report, sec); s != "" && !strings.EqualFold(s, "none") {
						fmt.Fprintf(&b, "  - %s: %s\n", sec, indent(s))
					}
				}
			}
			if m.Closed {
				if co, _, _ := l.Closeout(m.Name); co != "" {
					for _, sec := range []string{"Final report", "Follow-ups"} {
						if s := ledger.Section(co, sec); s != "" && !strings.EqualFold(s, "none") {
							fmt.Fprintf(&b, "- Closeout %s: %s\n", strings.ToLower(sec), indent(s))
						}
					}
					var docs []string
					for _, ln := range strings.Split(ledger.Section(co, "Doc updates"), "\n") {
						if strings.HasPrefix(ln, "### ") {
							docs = append(docs, strings.TrimSpace(ln[4:]))
						}
					}
					if len(docs) > 0 {
						fmt.Fprintf(&b, "- Docs updated: %s\n", strings.Join(docs, ", "))
					}
				}
			}
			for _, wt := range m.Worktrees {
				// Read the branch from the repo itself: closed workstreams' worktrees are gone.
				if commits := gitLog(filepath.Join(p.Root, wt.Repo), wt.Branch, since, 10); len(commits) > 0 {
					fmt.Fprintf(&b, "- Commits on %s (%s):\n", wt.Branch, wt.Repo)
					for _, c := range commits {
						fmt.Fprintf(&b, "  - %s\n", c)
					}
				}
			}
			b.WriteString("\n")
		}
	}
	section("Open workstreams", open)
	section("Closed in this period", closed)

	// Work done outside workstreams still belongs in a status update.
	var repoLines []string
	for _, repo := range p.Config.Workers.Repos {
		if commits := gitLog(filepath.Join(p.Root, repo), "", since, 8); len(commits) > 0 {
			repoLines = append(repoLines, fmt.Sprintf("- `%s` (current branch):", repo))
			for _, c := range commits {
				repoLines = append(repoLines, "  - "+c)
			}
		}
	}
	if len(repoLines) > 0 {
		b.WriteString("## Other repo activity\n\n" + strings.Join(repoLines, "\n") + "\n\n")
	}

	if f, err := os.Open(p.Path("backlog.md")); err == nil {
		var items []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			ln := sc.Text()
			if strings.HasPrefix(ln, "- [ ] ") && len(ln) >= 16 {
				if d, err := time.ParseInLocation("2006-01-02", ln[6:16], time.Local); err == nil && !d.Before(since.Truncate(24*time.Hour)) {
					items = append(items, ln)
				}
			}
		}
		f.Close()
		if len(items) > 0 {
			b.WriteString("## Backlog added\n\n" + strings.Join(items, "\n") + "\n\n")
		}
	}

	if pending := l.Pending(); len(pending) > 0 {
		b.WriteString("## Waiting on the user\n\n")
		for _, it := range pending {
			fmt.Fprintf(&b, "- %s: %s — %s\n", it.WS, it.Kind, it.Summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func firstLine(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }
func indent(s string) string    { return strings.ReplaceAll(s, "\n", "\n    ") }
func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
