# DONE — 01 — Spike: Engine VM containment and clean-disk start

Status: done

Resolved in: 2a08c35

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

- [x] A backend is found that meets every containment point above, or all three are shown not to, with the reason
- [x] The Docker API and a published container port are reachable over the agent's route
- [x] Clean-disk start plus image load timed on this Mac
- [ ] Linux viability assessed (tested on Linux, or the settings checked against QEMU/KVM documentation)
- [x] If the startup is far above 15 seconds, the user decides whether to keep the VM between **Sessions** instead (ADR 0001, PRD)

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Spike findings (macOS, Apple silicon, QEMU from Homebrew, HVF).**

**Backend: plain QEMU.** Lima (options 1 and 2) can't do it. Its built-in network always reaches the internet and the host (`host.lima.internal`), it has no restrict setting, and its QEMU options only cover the CPU type and minimum version, with no network passthrough. Plain QEMU with user-mode networking `restrict=on` works.

**Setup that worked:**
- Base disk: the Debian 13 genericcloud arm64 qcow2, checked against the sha512 in Lima's template, resized to 8 GB. Built by one online boot with a cloud-init seed. It installs `docker.io`, sets `net.ipv4.ip_local_port_range = 40000 40099` so Docker publishes ports only in that range, disables cloud-init afterwards and powers off (about 93 s). This is a base-image build step like `localllm build`, not a project's **Engine VM**. One later offline edit set up TLS and disabled `systemd-networkd-wait-online`.
- Every **Session** boot: `-snapshot` (a throwaway layer, so the base disk never changes), a direct kernel boot (`-kernel`/`-initrd` taken from the base disk, no UEFI or GRUB), `-netdev user,restrict=on` with `hostfwd` only for the Docker API (2376) and ports 40000-40099, all bound to 127.0.0.1. For the spike, SSH was forwarded too.
- dockerd listens on TCP 2376 with `--tlsverify`. The server certificate, key and CA come in through `-fw_cfg name=opt/localllm/...` and are read at boot from `/sys/firmware/qemu_fw_cfg`, so the base disk holds no keys and each project or **Session** can have its own.

**Containment (all blocked).** Tested as root over SSH, and again from a `--privileged --network host --pid host` container, each time with a default route added via `10.0.2.2` by root:
- an external IPv4 address (`1.1.1.1` HTTP and HTTPS, `8.8.8.8:53`) and an external IPv6 address;
- DNS through QEMU's resolver (`10.0.2.3`) and directly (`1.1.1.1`, `8.8.8.8`);
- the host through QEMU's host alias `10.0.2.2` (8080, the **Gateway** port; 22; the engine's own forwarded port);
- the Mac's LAN address, the LAN gateway, and Rancher's `192.168.5.2`;
- ICMP to the outside.

Only ICMP to QEMU's own virtual addresses (`10.0.2.2`, `10.0.2.3`) answers, and QEMU replies to that itself. Controls: `127.0.0.1:2376` and `10.0.2.15:2376` showed as open with the same check, so the probe method can tell open from blocked. (A third control using a web container was broken by the test itself, since the image has no `httpd`.)

**Agent route.** From a container on Rancher Desktop, through `host.docker.internal`: the Docker API answers over TLS, and a container published with `-P` got port 40000 and was reachable. A real testcontainers-go v0.44 test (`postgres.Run` with `postgres:16-alpine`, then a query) passed with `DOCKER_HOST=tcp://host.docker.internal:23760`, `DOCKER_TLS_VERIFY=1`, `TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal` and `TESTCONTAINERS_RYUK_DISABLED=true`. The container started in 0.75 s.

**Authentication is required (new requirement).** The forwards listen on the host's loopback, so any container on a normal Docker network can reach them, including another project's **Prep** containers. Without TLS, the Docker API was open to all of them. With `--tlsverify`, a client without a certificate is refused (TLS alert, or `400 Client sent an HTTP request to an HTTPS server`). Each project needs its own CA or client certificate, given only to its own **Sandbox**.

**Clean disk.** A file planted in `/etc` as root, and a loaded image, were both gone after the next boot.

**Timings on this Mac (2 vCPU, 2 GB):**

| Setup | Docker API up | With `postgres:16-alpine` loaded (114 MB archive) |
|---|---|---|
| UEFI + GRUB, TCP without TLS | 26.2 s | 28.2 s |
| UEFI + GRUB, TLS | 17.7 s | 19.7 s |
| Direct kernel boot, TLS | **3.3 s** | **5.4 s** |

dockerd deliberately delays its start by 15 s when it listens on TCP without TLS, and UEFI plus GRUB cost about 14 s. Linux itself boots in 3.6 s. Loading the image takes about 2 s.

**Linux viability (not tested on Linux).** Everything used is QEMU core, not macOS-specific: user-mode networking with `restrict=on` and `hostfwd`, `-snapshot`, direct kernel boot, and `fw_cfg`. On Linux the accelerator is `-accel kvm` instead of `hvf`, and an x86_64 host needs the amd64 image and kernel. Issue 09 tests it.

**Consequences for the plan:**
- ADR 0001, `CONTEXT.md` and the PRD say "Lima VM". They should say a QEMU VM managed by localllm. The decision itself (a per-project VM, never online, locked down from outside) stands.
- The PRD needs per-project TLS on the engine route, and the fixed published-port range.
- The base disk needs an online build step, separate from any project's **Engine VM**, like `localllm build`.

Spike files (base disk, kernel, certificates, test project) are in the session scratchpad and aren't part of the repo.

**2026-10-15 — Done.** Plain QEMU meets the containment requirement, and testcontainers works over the agent's route. Startup is 5.4 s, so the clean-disk-per-**Session** design stands, and the startup criterion needed no decision. ADR 0001, `CONTEXT.md` (new term **Engine base disk**), the PRD (new "Engine VM setup" section and stories 45-51) and issues 02, 03 and 07 were updated for QEMU, per-project TLS, the port range and the base disk build. Linux was not tested and its settings were not checked against documentation. The user deferred that to issue 09, with the rule that nothing is built macOS-only.
