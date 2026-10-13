# 10 — pi agent support

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- Install `@earendil-works/pi-coding-agent` in the base image, pinned, with `npm install -g --ignore-scripts` (requires Node ≥ 22.19).
- `run --agent pi` starts pi in the sandbox.
- ModelCatalog renders pi's provider config pointing at the in-sandbox gateway.
- pi state in its own per-project volume; pi base config read-only from the repo.
- pi has no permission system, so it's always treated as full auto: the dirty-tree `y` confirmation applies, and startup states that the sandbox is pi's only boundary.

## Acceptance criteria

- [ ] `localllm run --agent pi` starts pi and it can complete a prompt via the gateway
- [ ] pi sessions persist per project and are isolated between projects
- [ ] Dirty tree → `y` confirmation before pi starts
- [ ] Probes run before pi sessions, same as opencode
- [ ] Unit tests for the pi config rendering

## Blocked by

- `07-agent-config-and-state.md`
- `08-model-catalog.md`
- `09-approval-modes.md`
