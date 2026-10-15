# 02 — Tracer bullet: testcontainers Postgres passes in `localllm shell`

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

The thinnest end-to-end path. A project declares `postgres:16-alpine` under `engine.images` in `localllm.yaml`. `localllm prep` downloads it in the fetch phase, outside the **Engine VM**, with a registry tool, checked against its digest, into a per-project archive cache. `localllm shell` starts the project's **Engine VM** from a clean disk with the setup from issue 01, loads the archive, and points the **Sandbox** at it (`DOCKER_HOST`, `TESTCONTAINERS_HOST_OVERRIDE`, `TESTCONTAINERS_RYUK_DISABLED=true`). Inside, a testcontainers Postgres test passes offline. The VM stops when the shell exits, including on Ctrl-C.

The base image gains the Docker CLI and the registry tool. The **Sandbox** container keeps all its hardening, and the host's Docker socket stays unreachable. Projects without an `engine:` section are unchanged. VM names derive from the project hash, so two projects get separate VMs.

## Acceptance criteria

- [ ] `prep` downloads declared images into the project's archive cache, with no project code running
- [ ] `shell` starts the **Engine VM** from a clean disk with the images loaded, and stops it on exit and on interrupt
- [ ] The **Sandbox** environment points Docker and testcontainers at the **Engine VM**, with Ryuk disabled
- [ ] Projects without `engine:` start no VM and behave as before
- [ ] Unit tests for EngineConfig (minimal), EngineSpec and the SandboxSpec engine additions
- [ ] Integration test: a temp project with a testcontainers Postgres test passes offline in `shell` after `prep`
- [ ] Integration test: a file written in one **Session**'s **Engine VM** is gone in the next
- [ ] README section on the **Engine VM**

## Blocked by

- `01-spike-engine-vm-containment.md`
