---
status: accepted
---

# Agent containers run in a per-project Engine VM, not the host's Docker

Some projects' **Gates** need containers (testcontainers starting Postgres), so the agent needs a container engine inside the **Session**. Controlling an engine is root on whatever machine runs it, so we give each project that declares **Engine images** its own Lima VM. It is locked down from outside the VM to the same network containment as the **Sandbox**, never has a route out (images are downloaded during **Prep** outside it and loaded in), starts every **Session** from a clean disk, and is probed before every **Session**. This amends PRD stories 22 and 24 rather than weakening them: the host's Docker socket is still never reachable, and the **Sandbox** container still runs unprivileged.

## Considered Options

- **Host Docker socket behind a filtering proxy.** Rejected: the agent's containers would run on the host's engine, and one gap in the filter (a bind mount it misses) exposes the home directory.
- **Docker-in-Docker container next to the agent.** Rejected: it has to be privileged, and on Rancher Desktop a privileged container can take over the VM that mounts the home directory and reaches the host's loopback. On Linux it would be root on the host itself.
- **Rootless engine container (rootless Podman or Docker).** Not chosen: unprivileged, but it shares the kernel with the host's Docker and loosens the hardening of one container. Kept in mind if a VM proves too slow.
- **One shared Engine VM for all projects.** Rejected: concurrent **Sessions** of different projects could see and exec into each other's containers.
- **Engine VM online during Prep, offline during Sessions.** Rejected: something planted during a **Session** could run while the VM is online in the next **Prep**. "Never has a route out" is a simpler rule to enforce and probe.

## Consequences

- Network rules inside the Engine VM can't be trusted, because the agent has root there. Containment must come from the hypervisor's network setup on the host. Fallback order if Lima can't provide it: Lima on QEMU, then plain QEMU with user-mode networking `restrict=on`, then not shipping the feature.
- Lima (or QEMU) becomes a dependency for projects that use the engine. The design must work on Linux too (QEMU with KVM), so no macOS-only networking.
- A **Session** whose Engine VM can't start or fails a probe refuses to start. `--no-engine` is the explicit way around it, and the agent is told its container **Gates** can't run.
