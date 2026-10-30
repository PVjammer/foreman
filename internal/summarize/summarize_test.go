package summarize

import "testing"

func TestClean(t *testing.T) {
	cases := map[string]string{
		"\n\n\"Tests pass; PR ready.\"\nextra":       "Tests pass; PR ready.",
		"<think>hmm</think>\n- Waiting on DB choice": "Waiting on DB choice",
		`Updated tagline to "We build tools."`:       `Updated tagline to "We build tools."`,
		"":                                           "",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
