package ledger

import (
	"os"
	"testing"
	"time"

	"github.com/PVjammer/foreman/internal/project"
)

func newLedger(t *testing.T) *Ledger {
	root := t.TempDir()
	p := &project.Project{Root: root, Config: project.Defaults()}
	if err := os.MkdirAll(p.Path("ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(p)
}

func TestPendingKeepsNewestPerWorkstreamAndRanksBlockedFirst(t *testing.T) {
	l := newLedger(t)
	t0 := time.Now()
	l.AddItem(Item{WS: "a", Kind: "blocked", Time: t0})
	l.AddItem(Item{WS: "a", Kind: "done", Time: t0.Add(time.Second)}) // supersedes a's blocked
	l.AddItem(Item{WS: "b", Kind: "done", Time: t0.Add(2 * time.Second)})
	l.AddItem(Item{WS: "c", Kind: "question", Time: t0.Add(3 * time.Second)})

	got := l.Pending()
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(got), got)
	}
	if got[0].WS != "c" || got[1].WS != "b" || got[2].WS != "a" {
		t.Fatalf("want question first then newest done: %+v", got)
	}
	if got[2].Kind != "done" {
		t.Fatalf("a's blocked should be superseded by done, got %s", got[2].Kind)
	}

	l.Ack(got[0].ID)
	if n := len(l.Pending()); n != 2 {
		t.Fatalf("ack should remove one item, have %d", n)
	}
}

func TestReportStatusAndSections(t *testing.T) {
	l := newLedger(t)
	os.MkdirAll(l.P.WorkstreamDir("w"), 0o755)
	report := "---\nstatus: needs-input\n---\n## Summary\nDid a thing.\n\n## Open questions\nWhich DB?\n"
	os.WriteFile(l.ReportPath("w"), []byte(report), 0o644)

	body, status, _ := l.Report("w")
	if status != "needs-input" {
		t.Fatalf("status = %q", status)
	}
	if s := Section(body, "Open questions"); s != "Which DB?" {
		t.Fatalf("section = %q", s)
	}
	if s := Section(body, "Summary"); s != "Did a thing." {
		t.Fatalf("summary = %q", s)
	}
}

func TestKickoffRoundTrip(t *testing.T) {
	l := newLedger(t)
	os.MkdirAll(l.P.WorkstreamDir("w"), 0o755)
	if l.Kickoff("w") != "" {
		t.Fatal("expected no kickoff")
	}
	l.SetKickoff("w", "go")
	if l.Kickoff("w") != "go" {
		t.Fatal("kickoff not stored")
	}
	l.ClearKickoff("w")
	if l.Kickoff("w") != "" {
		t.Fatal("kickoff not cleared")
	}
}

func TestArchiveMovesClosedWorkstreamAndListFindsIt(t *testing.T) {
	l := newLedger(t)
	m := &Meta{Name: "auth", Kind: "feature", Created: time.Now()}
	if err := l.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(l.CloseoutPath("auth"), []byte("---\nstatus: applied\n---\n## Final report\nShipped.\n"), 0o644)
	m.Closed = true
	l.SaveMeta(m)
	if err := l.Archive("auth"); err != nil {
		t.Fatal(err)
	}
	if got := l.List(false); len(got) != 0 {
		t.Fatalf("open list = %d, want 0", len(got))
	}
	all := l.List(true)
	if len(all) != 1 || all[0].Name != "auth" {
		t.Fatalf("List(true) = %+v", all)
	}
	body, status, _ := l.Closeout("auth")
	if status != "applied" || Section(body, "Final report") != "Shipped." {
		t.Fatalf("closeout from archive: status %q body %q", status, body)
	}
}

func TestPendingRanksCloseoutAboveDone(t *testing.T) {
	l := newLedger(t)
	t0 := time.Now()
	l.AddItem(Item{WS: "a", Kind: "done", Time: t0.Add(time.Second)})
	l.AddItem(Item{WS: "b", Kind: "closeout", Time: t0})
	got := l.Pending()
	if len(got) != 2 || got[0].Kind != "closeout" {
		t.Fatalf("Pending = %+v", got)
	}
}
