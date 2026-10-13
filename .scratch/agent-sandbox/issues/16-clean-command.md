# 16 — `clean`: remove a project's volumes

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`localllm clean [dir]` removes that project's agent state volumes and dependency volumes after confirmation. Shared caches (Go module cache, pnpm store) are left alone unless an explicit flag is passed.

## Acceptance criteria

- [ ] Lists the volumes it will remove and asks for confirmation
- [ ] Afterwards, `run` starts with fresh state and `prep` is needed again
- [ ] Other projects' volumes are untouched
- [ ] Refuses while a session for that project is running

## Blocked by

- `07-agent-config-and-state.md`
- `12-prep-dependencies.md`
