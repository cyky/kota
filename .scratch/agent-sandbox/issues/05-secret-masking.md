# 05 — Secret masking with default ignore list and .localllmignore

Status: ready-for-agent

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

- [ ] A `.env` in the project reads as empty inside the sandbox and can't be written; the host file is unchanged
- [ ] Patterns from `.localllmignore` (including directory rules and negation) are masked
- [ ] Masked directories appear empty and read-only
- [ ] Startup warns when a masked file is tracked in git
- [ ] Mask scan on a project with a large `node_modules` stays fast (skips it)
- [ ] `check` lists the masks
- [ ] Table-driven unit tests with temp dirs and real git repos; integration test for the masked-read probe

## Blocked by

- `02-containment-probes-and-check.md`
