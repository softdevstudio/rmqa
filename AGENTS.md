# Instructions for coding agents (OpenAI Codex, ChatGPT and others)

Read CLAUDE.md first. It is the single source of truth for this repo's
workflow, state and to-do list, and the same rules apply to every agent.

The rules that matter most:

- `git pull` before you start: other agents push to main too.
- Work on main. No side branches, no pull requests to yourself.
- Never create or move release tags by hand. A release happens when
  VERSION, `const Version` in cmd/rmqa/main.go and a new CHANGELOG.md
  section are raised together and CI goes green on main.
- Run `gofmt -l .`, `go vet ./...` and `go test -race ./...` before pushing.
- Numbers on rmqa.com come from test/results, written by
  scripts/record-results.sh. Don't type numbers by hand.
- Finding codes are a public interface: never rename one.
- When you finish, update the State and Gaps sections of CLAUDE.md with
  what you did and what is still open.
