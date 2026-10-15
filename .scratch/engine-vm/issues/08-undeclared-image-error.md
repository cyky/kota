# 08 — Clear error for an undeclared image

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

When a test or the Docker CLI in a **Session** asks the **Engine VM** for an image that wasn't declared, the failure names the image and says it must be added under `engine.images` in `localllm.yaml` and prepped. The agent can then tell the user what to add, as it already does for dependencies.

## Acceptance criteria

- [ ] Starting an undeclared image fails with a message naming it and pointing at `localllm.yaml` and `prep`
- [ ] The **Engine VM** still never tries to download it
- [ ] Integration test with an undeclared image

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
