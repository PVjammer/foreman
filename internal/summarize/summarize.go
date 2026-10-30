// Package summarize turns a worker's screen tail and report into a one-line sidebar summary
// using a small local model. State is decided mechanically elsewhere; this only writes prose.
package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Summarizer struct {
	URL, Model string
	Enabled    bool
}

const prompt = `You write one-line status summaries for a sidebar that tracks coding-agent sessions.
Workstream: %s
Event: %s

Worker report (may be empty):
%s

Last lines of the agent's terminal:
%s

Reply with ONE line of at most 12 words saying what the agent did or what it needs from the user.
No preamble, no quotes, no markdown.`

func (s *Summarizer) Summarize(ws, event, report, screen string) (string, error) {
	if !s.Enabled {
		return "", fmt.Errorf("summarizer disabled")
	}
	body, _ := json.Marshal(map[string]any{
		"model":   s.Model,
		"prompt":  fmt.Sprintf(prompt, ws, event, clip(report, 2000), clip(screen, 3000)),
		"stream":  false,
		"think":   false,
		"options": map[string]any{"num_predict": 60, "temperature": 0.2},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.URL, "/")+"/api/generate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Error != "" {
		return "", fmt.Errorf("ollama: %s", r.Error)
	}
	return Clean(r.Response), nil
}

// Clean keeps the first non-empty line, strips quotes/markdown and caps the length.
func Clean(s string) string {
	if i := strings.Index(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "*#- "))
		for _, q := range []string{`"`, "'", "`"} {
			if len(ln) > 1 && strings.HasPrefix(ln, q) && strings.HasSuffix(ln, q) {
				ln = ln[1 : len(ln)-1]
			}
		}
		if ln != "" {
			if len(ln) > 90 {
				ln = ln[:87] + "..."
			}
			return ln
		}
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
