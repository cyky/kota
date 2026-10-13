# 06 — Post-session warning for changed files that run things

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **ChangeReport** module (`Snapshot(dir)` / `Diff(before, after) → warnings`). `run` snapshots before the session and prints a warning after it if any watched file was added, modified or deleted.

Watched: `package.json`, `Makefile`, `.github/workflows/`, `.vscode/`, `*.nix`, `flake.lock` (list easy to extend).

## Acceptance criteria

- [ ] After a session where the agent edited `package.json`, a warning lists it
- [ ] Added and deleted watched files are reported
- [ ] Unrelated changes produce no warning
- [ ] Snapshot is fast on large projects (only watched paths)
- [ ] Unit tests cover add/modify/delete and the no-change case

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
