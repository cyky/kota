# PRD: Engine VM for agent containers

Status: ready-for-agent

Decision record: `docs/adr/0001-engine-vm-for-agent-containers.md`. Terms as in `CONTEXT.md`.

## Problem Statement

Some of my projects run integration tests with testcontainers, for example starting a Postgres container per test run. Those tests are part of the project's **Gates**: an agent working unattended in a **Session** has to run them itself to know a task is done, without my input. Today the **Sandbox** has no container engine, on purpose: the host's Docker socket is never mounted and the container is unprivileged. So the agent can only run the unit tests, and either stops to ask me or declares a task done without its integration tests having run.

I can't give the agent the host's Docker. Controlling a Docker engine is root on the machine that runs it, which on my Mac is Rancher Desktop's VM, with my home folder mounted and my loopback services reachable. A privileged Docker-in-Docker container has the same problem. I want the agent to have a container engine whose worst case is a throwaway machine with nothing on it and no way out.

## Solution

Projects that need containers declare their **Engine images** in `localllm.yaml`. Each such project gets its own **Engine VM**: a small QEMU VM, managed by localllm, that runs only the containers the agent starts. Every **Engine VM** boots from one **Engine base disk** that `localllm build` makes.

- **Its network is locked down from outside the VM,** by the hypervisor's network setup on the host. The agent has root inside the VM, so rules inside it would mean nothing. It reaches only its own project's **Sandbox**: no internet, no DNS, no host loopback, no LAN. It never has a route out, not even during **Prep**.
- **Prep downloads the images,** outside the **Engine VM**, in its fetch phase, with no project code running. It checks them against recorded digests and keeps them as archives. **Prep**'s existing confirmation already covers changes to `localllm.yaml`.
- **Every Session starts the Engine VM from a clean disk** with the **Engine images** loaded, so nothing carries over between **Sessions**.
- **Probes check it before every Session,** like the **Sandbox**. A failure blocks the **Session**.
- **Only that project's Sandbox can use its Engine VM.** The Docker API requires a TLS client certificate made for that project, because the route passes through the host's loopback, which other containers can reach.
- **The agent's environment points Docker and testcontainers at its Engine VM.** testcontainers works without changes to the project's tests.
- **`--no-engine` starts a Session without the Engine VM on purpose.** The agent is then told its container **Gates** can't run, so it reports them as not run instead of passed.
- **It must work on Linux as well as macOS.** No macOS-only networking.

Projects without an `engine:` section are unchanged: no VM, no extra probes, no extra startup time.

## User Stories

### Declaring the engine

1. As a developer, I want to declare the container images my tests need in `localllm.yaml`, so that the agent can run them offline in a **Session**.
2. As a developer, I want a project without an `engine:` section to work exactly as today, so that only projects that need containers pay for an **Engine VM**.
3. As a developer, I want to use plain tags such as `postgres:16-alpine`, so that declaring an image is as easy as in a test file.
4. As a developer, I want the digest each tag resolved to recorded at **Prep**, so that I notice when a tag starts pointing at a different image.
5. As a developer, I want an invalid image reference or an unknown key in the `engine:` section rejected with a clear message, so that typos don't fail silently in a **Session**.
6. As a developer, I want to set the **Engine VM**'s CPU and memory in `localllm.yaml`, with a default of 2 CPUs and 2 GB, so that heavier projects can get more and my model keeps its memory.
7. As a developer, I want `localllm.yaml` to stay read-only inside the **Sandbox**, so that the agent can't add images or change the VM's size.

### Prep

8. As a developer, I want **Prep** to download the **Engine images** outside the **Engine VM**, so that the **Engine VM** never has a route out.
9. As a developer, I want the image download to run in **Prep**'s fetch phase with no project code running, so that downloading images can't run anything the agent wrote.
10. As a developer, I want each downloaded image checked against its digest, so that what is loaded later is exactly what was downloaded.
11. As a developer, I want **Prep** to ask me to type `y` when the declared images change, listing the added, removed and retagged images, so that an agent can't add an image without my review.
12. As a developer, I want image archives kept in a per-project cache, so that **Sessions** start without downloading anything.
13. As a developer, I want **Prep** to download only images that are new or whose tag moved, so that repeated **Preps** stay fast.
14. As a developer, I want a clear **Prep** error naming the image when a download fails, so that I know what to fix.

### Session start

15. As a developer, I want `localllm run` to start the project's **Engine VM** automatically when the project declares **Engine images**, so that I don't manage the VM myself.
16. As a developer, I want each **Session** to start the **Engine VM** from a clean disk, so that nothing the agent left in a previous **Session** affects this one.
17. As a developer, I want the **Engine images** loaded into the **Engine VM** at the start of every **Session**, so that the tests find them without a download.
18. As a developer, I want the extra startup time of a project with an **Engine VM** to stay around 15 seconds, so that the engine doesn't make **Sessions** painful to start.
19. As a developer, I want `localllm shell` to get the **Engine VM** too, so that I can run the container **Gates** by hand exactly as the agent would.
20. As a developer, I want the **Engine VM** stopped when the **Session** ends, including on Ctrl-C or a crash, so that idle VMs don't hold memory.
21. As a developer, I want a **Session** to refuse to start, with the reason and a fix, when the **Engine VM** can't start, its images haven't been prepped, the **Engine base disk** hasn't been built, or QEMU isn't installed, so that I never get an agent that silently can't run its **Gates**.
22. As a developer, I want `--no-engine` to start a **Session** without the **Engine VM**, so that I can let the agent work on something that doesn't need containers when the engine is unavailable.
23. As a developer, I want the agent told in its instructions and environment when the **Engine VM** is unavailable, so that it reports container **Gates** as not run instead of claiming the task is done.

### Containment

24. As a developer, I want the **Engine VM** unable to reach the internet, so that the agent can't send code out or pull untrusted images through it.
25. As a developer, I want the **Engine VM** unable to resolve external DNS names, so that DNS can't be used to send data out.
26. As a developer, I want the **Engine VM** unable to reach my host's loopback services or my LAN, so that it opens no route the **Sandbox** closes.
27. As a developer, I want the **Engine VM**'s network rules enforced outside the VM, so that the agent can't remove them even with root inside it.
28. As a developer, I want the **Engine VM** to have no host directories mounted, so that taking it over gives the agent nothing of mine.
29. As a developer, I want the agent to reach only its own project's **Engine VM**, on its Docker API and the ports its containers publish, so that the engine adds no other route.
30. As a developer, I want sessions in different projects to have separate **Engine VMs**, so that one project's agent can't see or exec into another project's containers.
31. As a developer, I want the host's Docker socket still never reachable from the **Sandbox**, so that the **Engine VM** doesn't weaken the existing protection.
32. As a developer, I want the **Sandbox** container to stay unprivileged with all capabilities dropped, so that using the engine doesn't loosen the agent's own container.

### Verification

33. As a developer, I want probes run inside the **Engine VM** before every **Session**, checking that the internet, external DNS, the host and the LAN are unreachable, so that a QEMU or OS update can't silently open the **Engine VM**.
34. As a developer, I want probes from the **Sandbox** checking that the **Engine VM**'s Docker API answers and that a container's published port is reachable, so that I know the engine is usable, not just closed.
35. As a developer, I want a failing **Engine VM** probe to block the **Session** the same way a **Sandbox** probe does, so that there's one rule for containment.
36. As a developer, I want `localllm check` to show the **Engine VM**'s status, its declared images and its probe results, so that I can inspect it without starting an agent.

### The agent's view

37. As a developer, I want `DOCKER_HOST` and `TESTCONTAINERS_HOST_OVERRIDE` set in the **Sandbox** to point at the **Engine VM**, so that testcontainers works without changes to the project's tests.
38. As a developer, I want testcontainers' Ryuk cleanup container disabled, so that no container needs the Docker socket mounted, and because localllm resets the **Engine VM** anyway.
39. As a developer, I want the Docker CLI in the base image, so that the agent can inspect its containers (`docker ps`, `docker logs`) while debugging a failing **Gate**.
40. As a developer, I want a test that starts an undeclared image to fail with a message naming the image and saying it must be declared in `localllm.yaml`, so that the agent can tell me what to add, as it already does for dependencies.

### Cleanup

41. As a developer, I want `localllm clean` to delete the project's **Engine VM** and its image archives, so that I can reclaim disk and reset the project.
42. As a developer, I want the **Engine VM**'s disks to live in localllm's own data directory, so that they're easy to find and never inside the project.

### Portability

43. As a developer, I want the **Engine VM** to work on Linux with KVM as well as on macOS, so that I can use localllm on a Linux machine later.
44. As a developer, I want a clear error on a Linux machine without `/dev/kvm`, so that I don't get an **Engine VM** too slow to use without knowing why.

### Engine base disk and the engine route (added after the spike)

45. As a developer, I want `localllm build` to build the **Engine base disk** once, online, from a digest-checked cloud image, so that every **Engine VM** boots from a known clean disk.
46. As a developer, I want the **Engine base disk** build to run no project code and to be separate from any project's **Engine VM**, so that "the **Engine VM** never has a route out" stays true.
47. As a developer, I want **Sessions** to warn when the **Engine base disk** is older than the current build definition, with the command that rebuilds it, as the project image does today, so that I pick up updates without them changing under a running project.
48. As a developer, I want each project's **Engine VM** to accept only a TLS client certificate made for that project, so that other containers on my machine, including another project's **Prep**, can't use it.
49. As a developer, I want the TLS material to reach the **Engine VM** at boot without being stored on the **Engine base disk**, so that the shared disk holds no keys.
50. As a developer, I want Docker in the **Engine VM** to publish container ports only in a fixed range, and only that range and the API forwarded, so that the agent's route opens nothing else.
51. As a developer, I want the **Engine VM** booted directly into its kernel, skipping firmware and boot menu, so that a **Session** with an engine starts in seconds.

## Implementation Decisions

### Order of work

- **The spike (issue 01) is done.** Findings are in its comments. In short: Lima can't restrict its network, and plain QEMU can. User-mode networking with `restrict=on` blocked the internet, DNS, the host and the LAN, even for root with a default route added. A real testcontainers Postgres test passed over the agent's route. A clean-disk boot with TLS and a direct kernel boot reached a ready Docker API in 3.3 s, and 5.4 s with `postgres:16-alpine` loaded. Not yet tested on Linux.

### Engine VM setup (from the spike)

- **QEMU, run directly by localllm.** No Lima.
- **Network:** `-netdev user,restrict=on`, with `hostfwd` bound to 127.0.0.1 only for the Docker API port and the published-port range. The agent reaches them through its forwarder, as it reaches the **Gateway**.
- **Disk:** `-snapshot` over the **Engine base disk**, so every boot is clean and the base disk never changes.
- **Boot:** direct kernel boot, with `-kernel` and `-initrd` taken from the **Engine base disk** at build time. No UEFI or GRUB, which cost about 14 s.
- **Engine API:** dockerd listens on TCP with `--tlsverify`. Without TLS, dockerd also delays its own start by 15 s on purpose. A per-project CA, server certificate and client certificate are made by localllm. The server side reaches the VM through `-fw_cfg` and is read at boot, and the client side goes only into that project's **Sandbox**.
- **Published ports:** the **Engine base disk** sets `net.ipv4.ip_local_port_range` to a small fixed range (40000-40099 in the spike), so `docker run -P` and testcontainers only use forwarded ports. Several projects' **Engine VMs** need different host-side ports, so localllm maps each VM's range to free host ports, and the forwarder maps them back to the in-VM numbers the agent sees.
- **Engine base disk build:** one online boot of a digest-checked Debian cloud image with a cloud-init seed. It installs Docker, sets the port range and the TLS service, disables cloud-init and `systemd-networkd-wait-online`, and powers off. Part of `localllm build`, like the base image.
- **Portability:** everything above is QEMU core and works the same on Linux, where the accelerator is KVM instead of HVF, and an x86_64 host needs amd64 artifacts. No macOS-only parts (vmnet, Virtualization.framework) are used.

### Modules

New deep modules, pure and testable without a VM:

- **EngineConfig.** `Parse(localllm.yaml engine section) → config | none | error`. It holds the image references and the VM size. It validates image references, rejects unknown keys and applies defaults (2 CPUs, 2 GB). An absent section means "no engine".
- **EngineSpec.** `Build(project, config, paths) → VM definition`. The definition has no host mounts, a network locked down from outside the VM, only the agent's ports forwarded, a throwaway disk layer over a clean base disk, and the configured size. Its output is the QEMU command line: the accelerator per host OS, `restrict=on`, the forwards, `-snapshot`, the direct kernel boot, and the `fw_cfg` entries. Per-project names derive from the same project hash as the volumes.
- **EngineImages.** `Plan(declared images, recorded digests) → downloads, changes, new record`. It works out which images to download, the added, removed and moved tags for **Prep**'s confirmation, and the digest record to store after a successful **Prep**.

Changed modules:

- **SandboxSpec.** For projects with an engine: the route from the **Sandbox** to its **Engine VM** (through a forwarder, as for the **Gateway**), `DOCKER_HOST`, `TESTCONTAINERS_HOST_OVERRIDE` and `TESTCONTAINERS_RYUK_DISABLED=true`. With `--no-engine`, an environment variable saying the engine is unavailable. The **Sandbox** container itself keeps all its hardening.
- **Probes.** A new engine probe set, run in a probe container inside the **Engine VM**. Must fail: an external IP, external DNS, the host's addresses and the LAN gateway. From the **Sandbox**, must succeed: the engine's Docker API, and a port published by a probe container. Short timeouts, like today's probes.
- **Agent config (opencode, pi).** A line in the agent's instructions saying whether the **Engine VM** is available, and that container **Gates** must be reported as not run when it isn't.
- **PrepGate.** Changes to the `engine:` section show up in the confirmation through EngineImages' change list.

Thin adapters and wiring:

- **EngineDriver.** A thin adapter over QEMU: start from the **Engine base disk**, wait for the TLS API, load image archives through the Docker API, stop, status.
- **EngineCerts.** Makes and stores the per-project CA, server and client certificates in localllm's data directory, outside the project and outside the **Sandbox**.
- **Engine base disk build.** Part of `localllm build`: download and check the cloud image, build the seed, run the one online boot, and keep the kernel and initrd next to the disk.
- **Prep.** The fetch phase downloads missing or moved images with a registry tool (for example `crane` or `skopeo`) into a per-project archive cache, checked against digests.
- **CLI.** `run` and `shell` start and stop the **Engine VM** around the **Session**, with stop on interrupt through the existing interrupt handling. `--no-engine` on `run` and `shell`. `check` shows engine status, images and probes. `clean` deletes the **Engine VM** and the archives.
- **Base image.** Adds the Docker CLI and the registry tool.

### Behaviour

- The **Engine VM** is opt-in: only projects with an `engine:` section get one.
- One **Engine VM** per project. It runs only while a **Session** of that project does, and the existing one-**Session**-per-project rule covers concurrency.
- Every **Session** start resets the VM's disk to the clean base and loads the archives.
- A failure to start, missing archives, a missing **Engine base disk** or QEMU, or a failed probe refuses the **Session**, unless `--no-engine` is given.
- Default VM size: 2 CPUs, 2 GB, configurable per project.

## Testing Decisions

- Good tests check external behaviour through module interfaces: given inputs, assert on the returned config, definition, plan or probe list. They don't assert on internal helpers or call order.
- **Unit tests (table-driven, no VM):**
  - EngineConfig: absent section, defaults, valid and invalid image references, unknown keys, size overrides.
  - EngineSpec: no host mounts ever; the network is restricted; only the agent's ports are forwarded; the throwaway disk layer is used; names are stable per project and differ across projects; the size is applied; forwards bind only to 127.0.0.1; the accelerator matches the host OS; the TLS material is passed through `fw_cfg`, never through a host mount.
  - EngineCerts: a client certificate signed for one project is not accepted by another project's CA.
  - EngineImages: first **Prep**, unchanged images, an added or removed image, a moved tag, and the record after success.
  - SandboxSpec: engine variables are present only for engine projects; `--no-engine` sets the unavailable signal; the **Sandbox** container is still unprivileged with no Docker socket.
  - Probes: the engine probe set covers internet, DNS, host and LAN, and the reachability probes cover the API and a published port.
  - Agent config: the instructions mention engine availability correctly in both states, for opencode and pi.
- **Integration tests**, tagged and skipped when QEMU or the **Engine base disk** isn't available, like the Docker integration tests:
  - Containment: boot a real **Engine VM** and assert every must-fail probe fails and every must-succeed probe succeeds, including from a privileged host-network container with a root-added default route.
  - Route authentication: a client without the project's certificate, and a client with another project's certificate, are refused.
  - Clean disk: something written in one **Session**'s **Engine VM** is gone in the next.
  - End to end: a temp Go or TypeScript project with a testcontainers Postgres test and `postgres` declared as an **Engine image**. Prep it, then run the test in a `shell` **Session**, and it passes offline.
- No tests for EngineDriver, Prep wiring or the CLI, which stay thin.
- Prior art: the table-driven unit tests across the internal packages, and the tagged Docker integration tests for the check, prep and project image paths.

## Out of Scope

- The host's Docker socket, filtered or not, in any form.
- Docker-in-Docker and rootless engine containers. They're recorded as alternatives in the ADR.
- One **Engine VM** shared by several projects.
- Downloading images during a **Session**, or a registry mirror reachable from the **Engine VM**.
- Running the project's tests during **Prep**.
- Docker Compose files from the project. Only containers started through the Docker API (testcontainers, the Docker CLI) are supported.
- Making all of localllm Linux-ready. Only the **Engine VM** must avoid adding macOS-only parts; the existing macOS assumptions (the LAN gateway probe, `host.docker.internal`) are separate work.
- Kubernetes, Kind, or GPU access in the **Engine VM**.

## Further Notes

- This amends stories 22 and 24 of the agent sandbox PRD (`.scratch/agent-sandbox/PRD.md`) without weakening them. The host's Docker socket stays out of reach, and the **Sandbox** container stays unprivileged. The new engine lives outside both.
- On this Mac, QEMU is installed through Homebrew. Lima is installed too, but isn't used.
- Linux support is tested later (issue 09). Until then, nothing in the engine code may assume macOS.
- testcontainers' Ryuk needs the Docker socket mounted into a container. Disabling it is safe here, because localllm resets the **Engine VM** at every **Session** start.
- Memory: the **Engine VM** runs alongside the local model, so its default size stays small.
