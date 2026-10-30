package views

import (
	"os"
	"testing"

	"github.com/PVjammer/foreman/internal/herdr"
	"github.com/PVjammer/foreman/internal/project"
)

func TestOutboxRoundTrip(t *testing.T) {
	p := &project.Project{Root: t.TempDir(), Config: project.Defaults()}
	h := herdr.New("no-such-session-for-tests") // agent list fails, so Send queues
	if queued, err := SendOrQueue(p, h, "w12:p3", "hello\nworld"); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	SendOrQueue(p, h, "w1:p1", "second")
	q := Outbox(p)
	if len(q) != 2 || q[0].Pane != "w12:p3" || q[0].Text != "hello\nworld" || q[1].Pane != "w1:p1" {
		t.Fatalf("outbox = %+v", q)
	}
	dropOutbox(p, "w12:p3")
	if q := Outbox(p); len(q) != 1 || q[0].Pane != "w1:p1" {
		t.Fatalf("after drop: %+v", q)
	}
}

func TestDocksPersist(t *testing.T) {
	p := &project.Project{Root: t.TempDir(), Config: project.Defaults()}
	os.MkdirAll(p.Path("logs"), 0o755)
	if len(Docks(p)) != 0 {
		t.Fatal("expected no docks")
	}
	saveDocks(p, []Dock{{Target: "scratch", Pane: "w1:p4"}})
	if d := Docks(p); len(d) != 1 || d[0].Target != "scratch" || d[0].Pane != "w1:p4" {
		t.Fatalf("docks = %+v", d)
	}
}
