# DONE — 05 — Secret masking with default ignore list and .localllmignore

Status: done
Resolved in: `2bf8cda`, `b01f5dc`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **MaskPlanner** module (`Plan(projectDir, rules) → masks + warnings`) wired into session startup.

- Built-in default rules: `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, `.npmrc`, `.netrc`, `.aws/`, and similar.
- Optional per-project `.localllmignore` in gitignore syntax, added on top of the defaults.
- Matching files are masked by an empty read-only file; matching directories by an empty read-only directory.
- Skip heavy directories (`node_modules`, `.git` objects, build output) during the walk.
- Warn when a masked file is tracked in git (risk of committing it emptied).
- Masks are computed at launch; say so in `check` output.
- Add a probe: reading a masked file returns no content.

## Acceptance criteria

- [x] A `.env` in the project reads as empty inside the sandbox and can't be written; the host file is unchanged
- [x] Patterns from `.localllmignore` (including directory rules and negation) are masked
- [x] Masked directories appear empty and read-only
- [x] Startup warns when a masked file is tracked in git
- [x] Mask scan on a project with a large `node_modules` stays fast (skips it)
- [x] `check` lists the masks
- [x] Table-driven unit tests with temp dirs and real git repos; integration test for the masked-read probe

## Blocked by

- `02-containment-probes-and-check.md`

## Comments

**2026-10-14 — Done.** Implemented in `2bf8cda` on `master`, extending `internal/mask`: `Plan(project, rules)` with `DefaultRules`.

Verified:
- The gitignore-style matcher's 35 cases are also run through `git check-ignore` and agree with git. This caught `vault/**` also matching the `vault` directory itself, which the matcher now does too.
- Unit tests with real files and git repos cover:
  - default rules and `.localllmignore` patterns (anchored, `**`, directory rules, negation)
  - nothing being re-included inside a masked directory
  - skipped heavy directories, plus a 10,000-file `node_modules` check that planning stays under 500 ms
  - symlinks: a target inside the project is masked; targets outside the project and dangling links are ignored
  - tracked-file warnings, including a tracked file inside a masked directory
  - a masked `.envrc`, and a masked directory dropping the protections inside it
- Integration tests (Rancher Desktop):
  - `.env`, `.aws/` and a `.localllmignore` directory rule read as empty inside the sandbox, while normal files read fine.
  - Writing or removing a mask, and editing `.localllmignore`, fail.
  - Host content is unchanged.
  - The masked probe fails when a mask is missing.
- `check` output, run against a fake home, lists the masks under "computed at launch" and warns about a tracked `.env.production`.

Not verified: `check` end to end on a real project. Issue 17 covers this.

Decisions:
- `.localllmignore` is read-only inside the sandbox; otherwise the agent could add `!.env` and see it next session. When the file is absent, an empty placeholder appears on the host during the session and is removed afterwards.
- The defaults unmask `.env.example`, `.env.sample`, `.env.template` and `.env.dist`, and `*.pub`.
- Not searched: `node_modules`, `.venv`, `venv`, `__pycache__`, `vendor`, `target`, `dist`, `build`, `.next`, `.nuxt`, `.gradle`, `.terraform`, `.direnv`, `.cache`, and `.git`.
- A mask takes over a protection on the same path (for example `.envrc` listed in `.localllmignore`). Protections inside a masked directory are dropped, since the mask already holds the directory read-only.
- `.localllmignore` is read only at the project root.

Not covered: `--ro` paths aren't masked.

Also fixed here, an issue 04 gap: an absolute `core.hooksPath` reached through a symlink outside the project went unprotected. A path inside the project as written is still checked for symlinks; only a path that lands inside after resolving through links outside the project is matched by its resolved form.

**2026-10-14 — Follow-up fix in `b01f5dc`.** A security review found that a mask over or around a protected path dropped the protection, and the directories above the mask weren't pinned. With `core.hooksPath` set to `tools/git/hooks` and `tools/git/` masked, the agent could rename `tools` and recreate the hooks. Parent directories of protected paths are now pinned before masking. Masks of ordinary secrets still don't pin their parents: renaming one only moves the empty mask, and the secret stays hidden.
