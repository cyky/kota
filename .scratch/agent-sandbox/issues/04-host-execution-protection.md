# 04 — Host-execution protection: read-only git hooks, git config, .envrc

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Prevent the agent from planting code that the host later executes.

- Bind-mount the project's `.git/hooks` and `.git/config` read-only over themselves.
- Bind-mount `.envrc` read-only; if it doesn't exist, mount an empty read-only placeholder so it can't be created.
- Git commit, branch and diff keep working inside the sandbox.
- Add probes: writes to `.git/hooks`, `.git/config` and `.envrc` must fail.
- Handle projects that aren't git repos (skip git mounts) and worktrees/submodules sensibly (document behaviour if not fully supported).

## Acceptance criteria

- [ ] Inside the sandbox, creating or modifying a file in `.git/hooks` fails
- [ ] `git config core.hooksPath x` (and other config writes) fails inside the sandbox
- [ ] Creating or editing `.envrc` fails; host `.envrc` content is unchanged
- [ ] `git add`, `git commit`, `git checkout -b`, `git diff` work inside the sandbox
- [ ] New probes run before every session and block on failure
- [ ] Non-git projects still start
- [ ] Unit tests for the mount plan; integration tests for the probes

## Blocked by

- `02-containment-probes-and-check.md`
