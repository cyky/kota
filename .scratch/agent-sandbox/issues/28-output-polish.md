# 28 — Output polish from the trial run

Status: needs-triage

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Small output problems found in the issue 17 trial run, done together:

1. **Probe columns.** In `check` and `run`, probe names longer than the name column push the "must fail" column out of line. Examples: `internet [2606:4700:4700::1111]:443`, `read-only /workspace/.localllmignore`, `read-only /etc/localllm/opencode.json`. Size the column to the longest name.
2. **Docker Compose progress.** `prep` and `run` print a garbled progress line every time (`[+]  1/1t 1/11`, `[+]  1/1te 1/1`), as if two updates overwrite each other. Find the cause, or switch these `docker compose` calls to plain or quiet progress output.
3. **Prep failure buried.** When the install fails, `localllm: prep install failed: exit status 1` comes after npm's full error and usage text (around 150 lines for an out-of-date lock file). Put a short summary after the install output: which command failed, and the first error line if it can be found.

## Acceptance criteria

- [ ] Probe output lines up for all current probe names
- [ ] No garbled progress lines from `prep` or `run`
- [ ] A failed prep ends with a summary naming the command that failed
- [ ] Tests updated

## Blocked by

None - can start immediately
