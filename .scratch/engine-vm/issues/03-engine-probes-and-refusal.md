# 03 — Engine probes and refusal

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

Before every **Session** of an engine project, probes run inside the **Engine VM** from a probe container. They must fail on an external IP, external DNS, the host's addresses and the LAN gateway. From the **Sandbox**, probes must succeed on the engine's Docker API and on a port published by a probe container. A failed probe blocks the **Session**, like a **Sandbox** probe. So does a VM that can't start, missing image archives, or a missing QEMU or **Engine base disk**, each with the reason and the fix.

`localllm check` shows the **Engine VM**'s status, declared images and probe results.

## Acceptance criteria

- [ ] Engine probe set covers internet, DNS, host and LAN (must fail) and the API and a published port (must succeed)
- [ ] Any engine probe failure, start failure, missing archive, or missing QEMU or **Engine base disk** refuses the **Session** with a fix
- [ ] `check` shows engine status, images and probes
- [ ] Unit tests for the engine probe set
- [ ] Integration test: every engine probe passes on a real **Engine VM**
- [ ] README probe list updated

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
