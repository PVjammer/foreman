// Package assets embeds the text that steers agents: the orchestrator guide, the brief
// template, tracker instructions and the herdr config snippet. Editing these files is the
// main way to change behaviour; the Go code stays mechanical.
package assets

import (
	"embed"
	"text/template"
)

//go:embed templates/*.md templates/*.toml templates/types
var FS embed.FS

var Templates = template.Must(template.ParseFS(FS, "templates/*.md", "templates/*.toml"))

func File(name string) string {
	b, _ := FS.ReadFile("templates/" + name)
	return string(b)
}
