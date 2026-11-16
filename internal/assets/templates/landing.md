{{define "landing-steps"}}This project uses **landing**: you finish by preparing your work to land, and the user decides with one press. foreman does the merge or opens the pull request; you never push, merge or open PRs yourself.
{{range .Plan}}
- `{{.Repo}}`: {{.How}}{{end}}

When the goal is done:

1. **Rebase and test.** Rebase each branch onto its base ({{range $i, $p := .Plan}}{{if $i}}, {{end}}`{{$p.BaseRef}}`{{end}}) if it has moved, resolve conflicts, and run the tests. If you can't fetch, rebase onto what you have; foreman checks again at Land.
2. **Commit everything.** Nothing may be left uncommitted in your worktrees.{{if eq .DocsMode "commit"}} Doc updates for `{{.Docs}}` go **on your branch** as commits, so they're reviewed with the code.{{else}} Repo docs (README, docs/) go on your branch as commits. Updates for the project docs folder `{{.Docs}}` (outside the repo) go in `{{.OutDir}}/docs.patch` instead: a unified diff with paths relative to that folder (`--- a/page.md` / `+++ b/page.md`, `/dev/null` for new files). foreman applies it once the work has landed. Only durable knowledge: decisions and why, how things work, gotchas.{{end}}
3. **Write `{{.OutDir}}/landing.md`**, exactly this shape (it becomes the pull request):

   ```markdown
   ---
   status: ready
   ---
   ## Title
   One line, imperative, like a commit subject.

   ## Description
   What changed and why, for a reviewer. Markdown.

   ## Tests
   The commands you ran and their results.

   ## Summary
   One to three lines for the user.
   ```
4. **Draft follow-ups** in `{{.OutDir}}/followups.jsonl`, one JSON object per line: `{"title": "...", "goal": "...", "type": "feature|bug|research|spike|poc|deploy", "mode": "interactive"}`. Only real next steps (at most 20); none is fine. The user picks start / queue / drop for each at Land.
5. **Set your report to `status: done`** and stop. foreman ends this session and asks the user to land. If landing hits a conflict or a failing check, or the user wants changes, a worker is restarted on the same branch with the details.
{{end}}[foreman → {{.M.Name}}] Time to land workstream "{{.M.Name}}". Don't do more work on the goal: prepare it to land.

{{template "landing-steps" .}}