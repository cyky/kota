# DONE — 03 — PathPolicy: safe project and read-only mounts

Status: done
Resolved in: `9488fa2`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A pure **PathPolicy** module (`Validate(project, roExtras, allowedRoot) → resolved paths | refusal`) wired into `run`, plus repeatable `--ro PATH` for read-only reference directories.

- Resolve symlinks before validating.
- Refuse `$HOME` itself, `/`, and anything outside the allowed root (default `~/Projects`, configurable).
- Never allow mounting `~/.ssh`, `~/.config`, or the Docker socket, including via `--ro`.
- `--ro` paths are mounted read-only at a predictable location inside the sandbox.

## Acceptance criteria

- [x] `localllm run ~` and `localllm run /` are refused with a clear message
- [x] A project outside the allowed root is refused; inside it is accepted
- [x] A symlink inside the allowed root that points outside it is refused
- [x] `--ro` works (repeatable), and writes to those paths fail inside the sandbox
- [x] `--ro ~/.ssh` (and other forbidden locations) is refused
- [x] Table-driven unit tests cover refusals, symlink escapes, and nested/overlapping paths
- [x] `check` shows the resolved paths

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `9488fa2` on `master`.

Verified:
- Unit tests (`internal/pathpolicy`) in a temporary fake home: refusals for home, `/`, the allowed root itself and outside the root; symlink escapes for both the project and `--ro`; `--ro` inside, around or equal to the project; forbidden directories, including a forbidden entry that is itself a symlink; duplicates; same-name targets; case-insensitive matching.
- CLI against a fake `HOME` in the scratchpad: every refusal in the acceptance criteria fires with a clear message, including `--ro ~/.SSH` and a symlink to `~/.ssh`. `check` lists the resolved sources and their `/ro/<name>` targets.
- Integration test `TestReadOnlyMountRejectsWrites` (Rancher Desktop): the new probe passes for a read-only mount and fails when the same mount is writable.

Not verified: the full `check --ro` flow end to end on a real project. The fake `HOME` hides the docker CLI plugins, and the real home was deliberately not used. Issue 17's trial run covers this.

Decisions beyond the issue text:
- The allowed root itself is refused, because it would expose every project at once.
- `--ro` paths don't have to be inside the allowed root, but must not contain `$HOME`.
- The forbidden list also covers `~/.gnupg`, `~/.aws`, `~/.kube`, `~/.docker`, `~/.rd` and `~/.colima`, as well as `/var/run/docker.sock` and `/run/docker.sock`.
- The allowed root is set only through `LOCALLLM_ALLOWED_ROOT`, with no flag.

For later issues: masks from 05 apply to `/workspace` only. That is why `--ro` refuses any path that overlaps the project; otherwise it would bypass the masks.
