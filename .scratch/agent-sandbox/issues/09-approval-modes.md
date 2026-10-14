# DONE — 09 — Approval modes: ask by default, --auto, dirty-tree confirmation

Status: done
Resolved in: `abb8725`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- opencode asks for approval by default (its normal permission prompts).
- `--auto` renders opencode permissions that approve edits and bash commands.
- Starting a full-auto session on a project with uncommitted changes asks the user to type `y`. Anything else aborts and suggests committing or stashing. A non-git project skips the check.
- The confirmation logic is agent-aware so pi (always full auto, slice 10) reuses it.

## Acceptance criteria

- [x] Default `run` → opencode prompts before edits and commands
- [x] `run --auto` → opencode acts without prompts
- [x] `run --auto` on a dirty tree asks for `y`; declining aborts without starting the container
- [x] Clean tree or ask mode → no confirmation
- [x] Unit tests for the rendered permissions (ModelCatalog/config renderer) and for the confirmation decision

## Blocked by

- `08-model-catalog.md`

## Comments

**2026-10-14 — Done.** Implemented in `abb8725` on `master`.

- `--auto` on `run` sets `opencode.Settings.Auto`, which renders `edit` and `bash` as `allow`. `webfetch` stays `deny`. Startup prints `approval: ask` or `approval: auto`.
- `internal/approval`: `FullAuto(agent, auto)` (pi always), `NeedsConfirmation(fullAuto, status)`, `ReadStatus(project)` (`git status --porcelain -- .` with fsmonitor and optional locks off; only changes inside the project count) and `Confirm` (lists up to 10 changes, only `y`/`Y` continues, reads byte by byte so the agent's stdin is left alone).
- The check runs after path validation and before Docker starts. The decline message suggests `git stash -u`, plus "or run without --auto" unless the agent is always full auto.
- The PRD puts `auto` on `ModelCatalog.Resolve`. The catalog stayed agent-neutral and the permissions live in the opencode renderer, since pi has none.

Verified:
- Unit tests: full auto per agent; confirmation decision for dirty, clean, non-git and ask mode; answers `y`, `Y`, `yes`, `n`, empty, EOF; prompt content and truncation; rest of stdin unread; `ReadStatus` on real repos with ignored files and subdirectories; rendered permissions for ask, auto, and auto without base permissions.
- Real runs with llama-swap in a throwaway repo: dirty + `--auto` with `n` exits 1 and creates no containers; with `y` starts; dirty in ask mode and clean with `--auto` don't ask.
- Ask mode: `opencode run` asked to create a file had both the write and the shell fallback "permission requested ... auto-rejecting"; no file. `--auto`: the file was created without asking.
- Closes the gap from issue 07: with an allow-everything `~/.config/opencode/opencode.json` written in the sandbox, ask mode still auto-rejected.

Not verified: the approval dialog in the interactive TUI (the evidence is from `opencode run`, which turns a prompt into a rejection).
