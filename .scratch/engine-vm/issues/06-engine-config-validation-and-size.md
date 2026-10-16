# DONE — 06 — `engine:` config validation and VM size

Status: done

Resolved in: 7768f90

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

The `engine:` section of `localllm.yaml` is validated. Invalid image references and unknown keys are rejected with a clear message. CPU and memory can be set, with defaults of 2 CPUs and 2 GB. The section is covered by the existing read-only `localllm.yaml` mount.

## Acceptance criteria

- [x] Invalid image references and unknown keys are rejected with a message naming them
- [x] CPU and memory are configurable, with defaults applied
- [x] The **Engine VM** starts with the configured size
- [x] Unit tests for EngineConfig: absent section, defaults, valid and invalid references, unknown keys, size overrides
- [x] README documents the section

## Blocked by

- `02-tracer-testcontainers-in-shell.md`

## Comments

### 2026-10-16

The `engine:` section is now parsed key by key: `images`, `cpus` (a whole number, at least 1) and `memory` (`4G`, `3072M`, `1 GiB`; at least 512M; a bare number is rejected). Errors name the key and value, and an unknown key lists the valid ones. Defaults stay at 2 CPUs and 2048 MiB.

Verified: `go test ./...` passes, including the new table cases. `go test -tags integration -run TestEngineInShell ./cmd/localllm/` passed (about 2 minutes, with real VM boots). The test project now declares `cpus: 3, memory: 3G`, and a subtest checks `docker info` in the VM reports 3 CPUs and about 3 GiB. That run wasn't verbose, so subtest results weren't seen individually.

Not changed: an invalid `engine:` section in a project never prepped with an engine doesn't block `run`/`shell` (existing behaviour; `prep` always refuses, `check` shows the error). The README states this. `localllm.yaml` was already mounted read-only; only the comment on that mount was updated.
