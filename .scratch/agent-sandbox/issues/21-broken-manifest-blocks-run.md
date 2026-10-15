# 21 — Don't let a broken manifest block run, shell and check

Status: ready-for-human

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Every session mode works out the dependency plan, not just `prep`. An unparseable `package.json`, `pnpm-workspace.yaml` or `localllm.yaml`, or a `packageManager` field that isn't a plain version, makes `run`, `shell` and `check` fail. If an agent leaves the manifest broken mid-edit, you can't start a session to fix it.

Mounting no dependency volumes is not a fix. The host's `node_modules` would then be visible and writable in the sandbox, so the agent could plant code there (and macOS binaries would mix with Linux ones).

Decision needed (HITL), then implement:

- **A.** Store the dependency directories of the last successful `prep` in its host-side record, and when detection fails outside `prep`, warn and reuse them. A project never prepped falls back to the root `node_modules` only.
- **B.** Keep refusing, with a message that names the broken file and how to fix it on the host.
- Something else.

`prep` keeps failing on a broken manifest in every option.

## Acceptance criteria

- [ ] Decision recorded in a comment on this issue
- [ ] With a broken `package.json`, `run`, `shell` and `check` behave as decided, and a host `node_modules` is never exposed in the sandbox
- [ ] `prep` still fails on a broken manifest
- [ ] Tests for the chosen behavior
- [ ] README updated

## Blocked by

None - can start immediately
