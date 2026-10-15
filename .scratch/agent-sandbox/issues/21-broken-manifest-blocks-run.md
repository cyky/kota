# DONE — 21 — Don't let a broken manifest block run, shell and check

Status: done

Resolved in: 5fe26d8

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

- [x] Decision recorded in a comment on this issue
- [x] With a broken `package.json`, `run`, `shell` and `check` behave as decided, and a host `node_modules` is never exposed in the sandbox
- [x] `prep` still fails on a broken manifest
- [x] Tests for the chosen behavior
- [x] README updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Decision: option A, plus covering stray `node_modules`.** When detection fails outside `prep`, sessions reuse the dependency directories of the last successful `prep` (only the root `node_modules` if there is no record), and also mount a volume over every other `node_modules` found on the host. Without that, a workspace package added since the last prep would show its host `node_modules` in the sandbox. `prep` keeps failing on a broken manifest.

**2026-10-15 — Done.** `prepgate.Record` has a new `Dirs` field, filled by `prep` after a successful install. `deps.Fallback` builds a plan with no install steps, with the recorded directories plus every host `node_modules`. This search skips only `.git` and `node_modules`, not `dist`, `vendor` and the like. Recorded paths are cleaned again, and symlinks are refused as in `Detect`. `run`, `shell` and `check` use it through `sessionDeps`. `printWarnings` shows the cause and says to fix the file and run `localllm prep`. A record from before this change has no `Dirs` and is treated like a never-prepped project. The README Dependencies section describes the fallback.

Verified: `TestFallback` (prepped and never-prepped, with strays under a workspace package, a new directory and `dist`, nested `node_modules` and `.git` ignored, no install steps), `TestFallbackRefusesSymlinks`, `TestBrokenManifestSession` (broken `package.json`, saved record and stray host `node_modules`: the run session mounts volumes over all three directories and warns, and `cmdPrep` fails with the parse error), and the `Dirs` round trip in `TestSaveLoad`. `go vet`, the unit suite and the full integration suite pass. Not covered by a test: the line in `cmdPrep` that copies the plan's directories into the record, since no test runs a real `prep` end to end.

Not changed: with a working manifest, a host `node_modules` outside the root and workspace packages (e.g. `tools/x/node_modules`) is still visible in the sandbox, as before.
