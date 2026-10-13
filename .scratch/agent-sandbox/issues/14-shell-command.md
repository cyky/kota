# 14 — `shell`: plain shell in the sandbox

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`localllm shell [dir]` opens an interactive shell in exactly the same sandbox as `run` (mounts, masks, networks, user, hardening), without starting an agent. Probes run first, same as `run`.

## Acceptance criteria

- [ ] `localllm shell` drops into `/workspace` as the host UID
- [ ] The same probes run and block the shell on failure
- [ ] Network and filesystem restrictions match `run` (unit test: specs differ only in the command)

## Blocked by

- `02-containment-probes-and-check.md`
