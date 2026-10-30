package digest

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "90m": 90 * time.Minute, "7d": 7 * 24 * time.Hour} {
		got, err := ParseSince(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if d := time.Since(got) - want; d < -time.Minute || d > time.Hour { // DST-tolerant for days
			t.Errorf("%s: off by %v", in, d)
		}
	}
	if _, err := ParseSince("soon"); err == nil {
		t.Error("expected error for bad input")
	}
}
