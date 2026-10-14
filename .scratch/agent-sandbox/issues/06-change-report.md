# DONE — 06 — Post-session warning for changed files that run things

Status: done
Resolved in: `38445f4`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **ChangeReport** module (`Snapshot(dir)` / `Diff(before, after) → warnings`). `run` snapshots before the session and prints a warning after it if any watched file was added, modified or deleted.

Watched: `package.json`, `Makefile`, `.github/workflows/`, `.vscode/`, `*.nix`, `flake.lock` (list easy to extend).

## Acceptance criteria

- [x] After a session where the agent edited `package.json`, a warning lists it
- [x] Added and deleted watched files are reported
- [x] Unrelated changes produce no warning
- [x] Snapshot is fast on large projects (only watched paths)
- [x] Unit tests cover add/modify/delete and the no-change case

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `38445f4` on `master` as `internal/changereport` (`Snapshot(dir)` / `Diff(before, after)` / `Warnings`). `run` takes the first snapshot before Docker starts and compares after the containers are gone and cleanup has run. Otherwise the `.envrc` placeholder would be reported as added.

Watch list (`changereport.Watched`): the issue's list, plus `.husky/`, `.githooks/` and `.envrc` at any depth (from issue 04), `makefile`/`GNUmakefile`, `.pre-commit-config.yaml` and `.devcontainer/` (its `initializeCommand` runs on the host). Files are compared by content hash, mode and symlink target. Dependency and build directories and `.git` are skipped.

Verified:
- Unit tests: add, modify, delete, no change, unrelated changes, permission change, same-size same-mtime edit, symlink retarget, watched directory replaced by a symlink, skipped dirs ignored, 20,000-file project under the 2 s timing check.
- Real Docker run (`--unsafe-skip-check`) on a throwaway project under `~/Library/Caches`: editing `package.json`, deleting `.vscode/tasks.json`, adding a workflow and changing `main.go` reported exactly the three watched files and no placeholder. A run that only changed `main.go` printed nothing.

Not verified: the integration suite (probes and mounts unchanged), a session with real opencode.

Limits, documented in the README: a warning, not a block; `check` doesn't report; nothing is reported if `localllm` is killed mid-session (cleanup doesn't run then either, as before).
