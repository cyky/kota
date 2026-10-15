# 01 — Spike: Engine VM containment and clean-disk start

Status: ready-for-human

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

Prove by hand, before anything is built into localllm, that an **Engine VM** can meet the PRD's containment requirement, and measure its startup. The result chooses the VM backend for every later issue.

Try in this order and stop at the first that works:

1. Lima with its own network settings.
2. Lima on QEMU (`vmType: qemu`).
3. Plain QEMU with user-mode networking `restrict=on` and explicit port forwards.

For the chosen setup, show:

- Network locked down from outside the VM. With root inside the VM (for example from a privileged container), these all fail: an external IP, external DNS, the host's loopback services (including the **Gateway** port), and the LAN gateway.
- The route the **Sandbox** would use reaches the Docker API in the VM, and a port published by a container in the VM. Nothing else is reachable.
- No host directories mounted.
- A start from a clean base disk with a throwaway layer, plus loading a `postgres:16-alpine` image archive, timed. Target: about 15 seconds.

Record the findings in this issue's comments: the backend, the exact settings, the timings, and anything that doesn't work on Linux (QEMU with KVM).

## Acceptance criteria

- [ ] A backend is found that meets every containment point above, or all three are shown not to, with the reason
- [ ] The Docker API and a published container port are reachable over the agent's route
- [ ] Clean-disk start plus image load timed on this Mac
- [ ] Linux viability assessed (tested on Linux, or the settings checked against QEMU/KVM documentation)
- [ ] If the startup is far above 15 seconds, the user decides whether to keep the VM between **Sessions** instead (ADR 0001, PRD)

## Blocked by

None - can start immediately
