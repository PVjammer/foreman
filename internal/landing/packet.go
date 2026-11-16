package landing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/PVjammer/foreman/internal/ledger"
)

// Limits on what foreman accepts from a worker's packet. Everything in it is untrusted:
// foreman parses it, caps it, and never runs any of it.
const (
	maxFile      = 1 << 20
	maxTitle     = 200
	maxBody      = 60000 // GitHub caps PR bodies at 65536
	maxFollowups = 20
	maxGoal      = 2000
)

// Packet is what a worker leaves behind when its work is ready to land.
type Packet struct {
	Status      string // "ready" when complete
	Title       string
	Description string
	Tests       string
	Summary     string
	Followups   []Followup
	DocsPatch   []byte // foreman-docs edits (docs modes local/external), applied at landed
	Skipped     []string
}

// Followup is one drafted follow-up workstream.
type Followup struct {
	Title string `json:"title"`
	Goal  string `json:"goal"`
	Type  string `json:"type"`
	Mode  string `json:"mode"`
}

// OutDir is where the worker writes its packet. It is the workstream's out/ folder once
// that exists (ws-control-isolation adds it), otherwise the workstream folder itself.
func OutDir(l *ledger.Ledger, name string) string {
	dir := l.P.WorkstreamDir(name)
	if fi, err := os.Lstat(filepath.Join(dir, "out")); err == nil && fi.IsDir() {
		return filepath.Join(dir, "out")
	}
	return dir
}

func PacketPath(l *ledger.Ledger, name string) string {
	return filepath.Join(OutDir(l, name), "landing.md")
}

// readUntrusted reads a regular file without following symlinks or blocking on FIFOs,
// capped at maxFile. A missing file is (nil, nil).
func readUntrusted(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxFile)
	}
	return b, nil
}

// clean strips control characters (ANSI escapes included) and caps length. multiline keeps
// newlines and tabs.
func clean(s string, max int, multiline bool) string {
	s = strings.ReplaceAll(s, "\x1b", "")
	s = strings.Map(func(r rune) rune {
		if multiline && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// ReadPacket reads and validates the worker's packet. A missing landing.md returns
// (nil, nil): the worker hasn't prepared landing.
func ReadPacket(l *ledger.Ledger, name string) (*Packet, error) {
	dir := OutDir(l, name)
	b, err := readUntrusted(filepath.Join(dir, "landing.md"))
	if err != nil || b == nil {
		return nil, err
	}
	md := string(b)
	p := &Packet{
		Status:      frontmatterStatus(md),
		Title:       clean(firstLine(ledger.Section(md, "Title")), maxTitle, false),
		Description: clean(ledger.Section(md, "Description"), maxBody, true),
		Tests:       clean(ledger.Section(md, "Tests"), 4000, true),
		Summary:     clean(ledger.Section(md, "Summary"), 4000, true),
	}
	if p.Title == "" {
		return p, errors.New("landing.md has no ## Title")
	}
	fb, err := readUntrusted(filepath.Join(dir, "followups.jsonl"))
	if err != nil {
		p.Skipped = append(p.Skipped, err.Error())
	}
	p.Followups, p.Skipped = parseFollowups(fb, p.Skipped)
	db, err := readUntrusted(filepath.Join(dir, "docs.patch"))
	if err != nil {
		p.Skipped = append(p.Skipped, err.Error())
	} else if len(bytes.TrimSpace(db)) > 0 {
		p.DocsPatch = db
	}
	return p, nil
}

func parseFollowups(b []byte, skipped []string) ([]Followup, []string) {
	var out []Followup
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n++
		if len(out) >= maxFollowups {
			skipped = append(skipped, fmt.Sprintf("follow-ups past %d ignored", maxFollowups))
			break
		}
		var f Followup
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			skipped = append(skipped, fmt.Sprintf("follow-up line %d isn't JSON", n))
			continue
		}
		f.Title = clean(f.Title, maxTitle, false)
		f.Goal = clean(f.Goal, maxGoal, true)
		f.Type = clean(f.Type, 32, false)
		if f.Mode != "autonomous" {
			f.Mode = "interactive"
		}
		if f.Title == "" {
			skipped = append(skipped, fmt.Sprintf("follow-up line %d has no title", n))
			continue
		}
		if !validType(f.Type) {
			f.Type = ""
		}
		out = append(out, f)
	}
	if sc.Err() != nil {
		skipped = append(skipped, "followups.jsonl: a line is too long; stopped there")
	}
	return out, skipped
}

func validType(t string) bool {
	if t == "" || len(t) > 32 {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func frontmatterStatus(md string) string {
	if !strings.HasPrefix(md, "---") {
		return ""
	}
	end := strings.Index(md[3:], "---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(md[3:3+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "status" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}
