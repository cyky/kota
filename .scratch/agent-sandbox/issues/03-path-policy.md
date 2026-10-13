# 03 — PathPolicy: safe project and read-only mounts

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A pure **PathPolicy** module (`Validate(project, roExtras, allowedRoot) → resolved paths | refusal`) wired into `run`, plus repeatable `--ro PATH` for read-only reference directories.

- Resolve symlinks before validating.
- Refuse `$HOME` itself, `/`, and anything outside the allowed root (default `~/Projects`, configurable).
- Never allow mounting `~/.ssh`, `~/.config`, or the Docker socket, including via `--ro`.
- `--ro` paths are mounted read-only at a predictable location inside the sandbox.

## Acceptance criteria

- [ ] `localllm run ~` and `localllm run /` are refused with a clear message
- [ ] A project outside the allowed root is refused; inside it is accepted
- [ ] A symlink inside the allowed root that points outside it is refused
- [ ] `--ro` works (repeatable), and writes to those paths fail inside the sandbox
- [ ] `--ro ~/.ssh` (and other forbidden locations) is refused
- [ ] Table-driven unit tests cover refusals, symlink escapes, and nested/overlapping paths
- [ ] `check` shows the resolved paths

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
