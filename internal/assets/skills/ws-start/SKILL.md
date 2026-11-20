---
name: ws-start
description: Start a foreman workstream. Turns a request for a feature, bug fix, investigation, spike, proof of concept or deploy into a ticket-quality goal, picks type, profile, repos and name, and runs `foreman ws new`. Use when the user asks for work to be done in a foreman project ("start a workstream for X", "can someone fix Y", "look into Z"), or on a `Workstream request from the user:` or `The user approved follow-up` message from foreman. Orchestrator only.
---

# Start a workstream

Orchestrator only. If you are a workstream worker (you have a `brief.md`), this isn't for you: stop.

The goal you write becomes the worker's whole brief. A vague goal wastes a session, so spend the effort here.

## 1. Understand the request

- Small questions about the project you answer yourself; this skill is for work.
- If the request is ambiguous about *what done looks like* or *which part of the system*, ask the user one or two questions first. Don't ask about things you can look up (repos, existing docs, the backlog).
- One task, one workstream. A follow-up to earlier work that is more than a small tweak gets a new workstream, seeded with the earlier one's report (`foreman ws show <old>`), not a message to the old session.
- If the user named a ticket, read it through whatever `.foreman/tracker.md` says to use, and pass it as `--ticket`.

## 2. Pick the mechanics

Run `foreman types` and `foreman profiles` if you don't have them in context.

- **Type**: by what the work *is*, not its size. Code that ships → `feature`; something broken → `bug`; a question to answer → `research`; a time-boxed try → `spike`; a throwaway demo → `poc`; releasing → `deploy`. Custom types may exist; their descriptions say when to use them.
- **Profile**: by its "use for" note. A small local model for lookups and mechanical edits, a stronger one for features and tricky bugs. Leave `--profile` out to use the default. If the user names a harness or model ("use pi"), use that.
- **Repos** (`--repos a,b`): the repos the worker will change, relative to the project root. Types with worktrees get one worktree per repo; without `--repos` the worker runs at the project root. The repo list is in `.foreman/ORCHESTRATOR.md`.
- **Name**: lowercase, short, specific: `auth-timeout`, `csv-export`, `deploy-1-4`. Must match `[a-z][a-z0-9_-]{0,31}` and not already exist (`foreman ws list --all`).

## 3. Write the goal

Write it like a good ticket, in this order:

1. **What and why**, in a sentence or two.
2. **Done looks like**: concrete, checkable outcomes.
3. **Where**: repos, directories or components involved, if known.
4. **Constraints**: what not to touch, branches, compatibility, deadlines, anything the user said.
5. **Verify**: how the worker proves it's done (tests to run, command to try, what to show).
6. **Context**: links, ticket, earlier workstream reports, relevant docs pages.

Don't repeat what the brief template already says (report format, check-in rules, staying in scope). Keep it to what this task needs.

## 4. Start it

```bash
foreman ws new <name> --type <type> [--profile <p>] [--repos a,b] [--ticket KEY] --goal "<goal>"
```

- Quote a multi-line goal with a heredoc so nothing in it is interpreted by the shell:

  ```bash
  foreman ws new csv-export --type feature --repos api --goal "$(cat <<'GOAL'
  Add CSV export to the reports page so finance can pull monthly numbers.
  Done: ...
  GOAL
  )"
  ```
- To show the user first, or when unsure, add `--dry-run`. It prints the resolved type, profile, worktrees and the full brief, and creates nothing. Run it again without `--dry-run` once the user agrees.
- Landing projects (`foreman landing status`): a new workstream ends by landing unless you pass `--no-landing`. Pass it only if the user asks for the old closeout flow.
- For a request from foreman's new-workstream box, start it directly unless it's too ambiguous to write a goal; then ask the user.
- For `[foreman → orchestrator] The user approved follow-up <id> …`: the user already chose to start it. Write the goal from the follow-up's title and goal plus the source workstream's report (`foreman ws show <from>`). Use the suggested type unless it says "(pick one)". The message also gives a mode (interactive or autonomous). Pass it if `foreman ws new --help` has a flag for it; otherwise start it normally and tell the user the mode wasn't applied. Then run `foreman proposals started <id>`.

Then tell the user in one or two lines: the name, type and profile, and what the worker will do first. Don't paste the brief.
