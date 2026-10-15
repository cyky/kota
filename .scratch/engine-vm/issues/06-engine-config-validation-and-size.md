# 06 — `engine:` config validation and VM size

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

The `engine:` section of `localllm.yaml` is validated. Invalid image references and unknown keys are rejected with a clear message. CPU and memory can be set, with defaults of 2 CPUs and 2 GB. The section is covered by the existing read-only `localllm.yaml` mount.

## Acceptance criteria

- [ ] Invalid image references and unknown keys are rejected with a message naming them
- [ ] CPU and memory are configurable, with defaults applied
- [ ] The **Engine VM** starts with the configured size
- [ ] Unit tests for EngineConfig: absent section, defaults, valid and invalid references, unknown keys, size overrides
- [ ] README documents the section

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
