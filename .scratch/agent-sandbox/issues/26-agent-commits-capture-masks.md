# 26 — Agent commits capture mask placeholders

Status: needs-triage

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Found in the issue 17 trial run. pi, in full auto, committed its README edit with `git add -A`. The commit also contained:

- the empty files mounted over paths that don't exist on the host (`.envrc`, `.kotaignore`, `kota.Dockerfile`, `kota.yaml`), added as new empty files;
- the masked `.env` (untracked on the host, not in `.gitignore`), added as an empty file;
- the masked, tracked `.npmrc`, recorded as emptied;
- `.claude/settings.local.json`, which the host ignores only through the global excludes file `~/.config/git/ignore`. The sandbox doesn't have that file.

No secret leaked, but pushing the commit would delete or empty files in the repo, and add junk files to it. The change report only looks at files in the working tree, so it said nothing about the commit.

Two parts, to decide in triage:

1. **Keep the placeholders and masks out of commits made in the sandbox.** Options:
   - Point `core.excludesFile` (through the `GIT_CONFIG_*` variables in `gitEnv`) at a generated file. It would list the placeholder and masked paths and include the host's global excludes. This only covers untracked paths, so the tracked `.npmrc` would still be recorded as emptied.
   - Check new commits after the session for placeholder or masked paths and warn about them.
2. **Report new commits in the change report.** Record `HEAD` at session start. On exit, list the commits made in the session, and flag any that touch placeholder, masked or watched paths.

## Acceptance criteria

- [ ] After a session in which the agent runs `git add -A && git commit`, either the commit contains no placeholder or masked paths, or kota warns that it does
- [ ] The change report lists the commits made during the session
- [ ] Files ignored by the host's global excludes behave the same inside the sandbox, or the README says they don't
- [ ] Integration test for an agent commit made with `git add -A`

## Blocked by

None - can start immediately
