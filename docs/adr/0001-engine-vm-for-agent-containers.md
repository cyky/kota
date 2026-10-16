---
status: accepted
---

# Agent containers run in a per-project Engine VM, not the host's Docker

Some projects' **Gates** need containers (testcontainers starting Postgres), so the agent needs a container engine inside the **Session**. Controlling an engine is root on whatever machine runs it, so each project that declares **Engine images** gets its own QEMU VM, managed by kota. QEMU's user-mode networking with `restrict=on` locks it down from outside the VM, to the same network containment as the **Sandbox**. It never has a route out: images are downloaded during **Prep** outside it and loaded in. It boots every **Session** from the **Engine base disk** through a throwaway layer, and is probed before every **Session**. Its Docker API needs a per-project TLS client certificate. This amends PRD stories 22 and 24 rather than weakening them: the host's Docker socket is still never reachable, and the **Sandbox** container still runs unprivileged.

## Considered Options

- **Host Docker socket behind a filtering proxy.** Rejected: the agent's containers would run on the host's engine, and one gap in the filter (a bind mount it misses) exposes the home directory.
- **Docker-in-Docker container next to the agent.** Rejected: it has to be privileged, and on Rancher Desktop a privileged container can take over the VM that mounts the home directory and reaches the host's loopback. On Linux it would be root on the host itself.
- **Rootless engine container (rootless Podman or Docker).** Not chosen: unprivileged, but it shares the kernel with the host's Docker and loosens the hardening of one container. Kept in mind if a VM proves too slow.
- **Lima.** Rejected after the spike: its built-in network always reaches the internet and the host (`host.lima.internal`), and it has no setting to restrict that or to pass network options to QEMU.
- **One shared Engine VM for all projects.** Rejected: concurrent **Sessions** of different projects could see and exec into each other's containers.
- **Engine VM online during Prep, offline during Sessions.** Rejected: something planted during a **Session** could run while the VM is online in the next **Prep**. "Never has a route out" is a simpler rule to enforce and probe.

## Consequences

- Network rules inside the Engine VM can't be trusted, because the agent has root there. Containment comes from QEMU on the host. The spike checked it as root and from a privileged host-network container, with a root-added default route.
- QEMU's port forwards listen on the host's loopback, which any container on a normal Docker network can reach. So the Docker API requires TLS with a per-project client certificate, given only to that project's **Sandbox**. Certificates reach the VM through QEMU's `fw_cfg`, so the **Engine base disk** holds no keys.
- Docker publishes container ports only in a fixed range set in the **Engine base disk**, and only that range and the API port are forwarded.
- kota builds the **Engine base disk** once, online, like `kota build`. That build is not a project's **Engine VM** and runs no project code.
- QEMU becomes a dependency for projects that use the engine. The design must not be macOS-only: QEMU's user-mode networking, `-snapshot`, direct kernel boot and `fw_cfg` are the same on Linux, where the accelerator is KVM instead of HVF.
- A **Session** whose Engine VM can't start or fails a probe refuses to start. `--no-engine` is the explicit way around it, and the agent is told its container **Gates** can't run.
