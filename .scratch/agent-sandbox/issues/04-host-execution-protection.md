# DONE — 04 — Host-execution protection: read-only git hooks, git config, .envrc

Status: done
Resolved in: `1e44af2`

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

- [x] Inside the sandbox, creating or modifying a file in `.git/hooks` fails
- [x] `git config core.hooksPath x` (and other config writes) fails inside the sandbox
- [x] Creating or editing `.envrc` fails; host `.envrc` content is unchanged
- [x] `git add`, `git commit`, `git checkout -b`, `git diff` work inside the sandbox
- [x] New probes run before every session and block on failure
- [x] Non-git projects still start
- [x] Unit tests for the mount plan; integration tests for the probes

## Blocked by

- `02-containment-probes-and-check.md`

## Comments

**2026-10-14 — Done.** Implemented in `1e44af2` on `master`. The mounts are planned by `internal/mask` (`Plan(project)`), which issue 05 should extend with secret masking.

Verified:
- Unit tests with real git repos in temp dirs: non-git project, existing and missing `.envrc`, missing hooks dir, `core.hooksPath` (relative, absolute, from global config, outside the project, the default), `extensions.worktreeConfig`, linked worktree, submodule, symlink refusals, placeholder cleanup.
- Integration tests (Rancher Desktop) in throwaway repos under `~/Library/Caches`:
  - All probes pass with protection applied, and the hooks, config, `.envrc` and pinned `.git` probes fail without it.
  - Inside the sandbox, these fail: planting or editing a hook, `git config core.hooksPath`, `git config core.fsmonitor`, writing or removing `.envrc`, `mv .git`.
  - add, commit, `checkout -b` and diff work, and the host sees the new branch.
  - A host `.envrc` keeps its content. A non-git project starts. The `.envrc` placeholder is removed after the session.

Not verified: `localllm check` on a real project. Real home directories were deliberately not used; issue 17's trial run covers this.

Findings:
- Read-only mounts alone could be bypassed: renaming `.git` and creating a new one with hooks worked. `.git` is now bind-mounted over itself (a mount point can't be renamed or removed), and a probe checks it.
- Git did not work inside the sandbox at all, a gap left from 01. The shared mount shows files as owned by root, so git refused with "dubious ownership". `safe.directory=*` and the host `user.name`/`user.email` are passed in through `GIT_CONFIG_*` (command-line scope, which git trusts).
- Docker creates the mount point for a placeholder on the host, so an empty `.envrc` appeared in the project. Placeholders are removed after the session if they are still empty.

Limits, documented in the README:
- Non-git project: a repository the agent creates with `git init` is not protected.
- A worktree or a submodule checkout opened on its own has a `.git` file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call (husky's `.husky/pre-commit`) and `.envrc` files in subdirectories stay writable.

For 06: add `.husky/`, `.githooks/` and `**/.envrc` to the watch list.
