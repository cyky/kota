# Engine VM

Some projects' tests start containers, for example testcontainers with Postgres. The sandbox has no Docker of its own, and never gets the host's: controlling a Docker engine is root on the machine that runs it. A project that needs containers declares the images in `kota.yaml` instead, and gets its own **Engine VM**: a small QEMU VM, managed by kota, that runs only the containers started in its sessions. Why it is a VM is in [adr/0001-engine-vm-for-agent-containers.md](adr/0001-engine-vm-for-agent-containers.md).

```yaml
engine:
  images: [postgres:16-alpine]   # plain references with an optional tag
  cpus: 2                        # optional, default 2
  memory: 2G                     # optional, default 2G; M or G (MiB or GiB), at least 512M
```

`images` is required and lists each image once. Each must be a valid Docker reference with a tag or none (`latest`), never a digest: prep records the digest the tag resolves to. `cpus` is a whole number of at least 1. The VM runs next to your model, so keep it small. An invalid reference, a size without its unit or out of range, or an unknown key in the section is rejected with a message naming it: `prep` refuses, and so do `run` and `shell` once the project has been prepped with an engine (see below). `kota.yaml` is mounted read-only in the sandbox, so the agent can't declare images or resize the VM.

`kota prep` downloads the images with crane, after the dependency fetch and with no project code running: crane runs in a throwaway container of the base image that has only the project's new archive directory mounted, not the project. Each tag is resolved to its digest and the image pulled by that digest, so every manifest and layer is checked against it. The archives and the digests they were pulled by are kept in `kota/engine/projects/<hash of the project path>/images/` under the user cache dir, outside the project and the sandbox.

Each later `prep` first looks up, with crane in the same kind of container, which digest each tag of the last prep points to now. It looks up only the images the last prep recorded: an image newly declared in `kota.yaml`, which an agent may have written, isn't sent anywhere until you confirm. Then prep asks you to type `y` when the images changed, even if no file did, listing them:

```
Engine images, downloaded with crane (no project code runs):
  new      redis:7
  moved    postgres:16-alpine: sha256:3f5a9c1e2b7d -> sha256:9b04e8d2c6a1
  removed  nginx:1.27
```

Only new images, images whose tag moved (pulled by the digest shown) and images whose archive is gone are downloaded; the others are kept as they are. A failed lookup or download names the image and keeps the previous archives.

`kota run` and `kota shell` start the project's Engine VM, for opencode and pi alike, wait for its Docker API, load the archives, and point the sandbox at it:

- `DOCKER_HOST=tcp://engine:2376`, `DOCKER_TLS_VERIFY=1` and `DOCKER_CERT_PATH=/etc/kota/engine`, so the Docker CLI (in the base image) and testcontainers use it;
- `TESTCONTAINERS_HOST_OVERRIDE=engine`, so tests connect to their containers' published ports through the same route;
- `TESTCONTAINERS_RYUK_DISABLED=true`. Ryuk would need the Docker socket mounted into a container, and the VM is reset every session anyway.

The tests need no changes. The VM stops when the session ends, including on Ctrl-C, SIGTERM or a closed terminal. If kota itself is killed outright, the next command for the project stops the VM, found through its pidfile. `kota check` starts it too: it lists the declared images with the digests prep recorded, says why the VM can't start if it can't, and shows the Engine VM probes after the sandbox's. Projects without an `engine:` section start no VM and behave as before.

The agent is told whether it has the Engine VM, so it doesn't claim its container tests passed when they couldn't run. In an engine project, `KOTA_ENGINE` is `available` or `unavailable`, and a matching note is added at the end of the agent's instructions (see [What the agent is told](agents.md#what-the-agent-is-told)). pi reads a project's `.pi/APPEND_SYSTEM.md` instead only in trusted projects, and kota trusts none.

The VM never downloads an image. A test or `docker` command that needs an image not declared under `engine.images` fails when Docker tries to pull it, with Docker's own error, a failed lookup of the registry (`lookup registry-1.docker.io: Temporary failure in name resolution`), within seconds. Nothing reaches a registry. The Docker CLI names the image first (`Unable to find image 'redis:7' locally`); testcontainers' error doesn't. That error doesn't mention kota, so the agent's note lists the declared images, explains what such a failure means, tells the agent to find the image in the failing test when the error doesn't name it, and not to retry or work around it. It should report the affected tests as unable to run, and ask you to add the image to `kota.yaml` and run `kota prep`.

`--no-engine` on `run` or `shell` starts the session without the Engine VM on purpose, for work that doesn't need containers, or when the VM can't start (no QEMU, no base disk, images not prepped). No VM, engine forwarder, certificates or `DOCKER_HOST` are set up, startup says so, and the agent is told that tests needing containers can't run and must be reported as not run, never as passed. In a project without an `engine:` section, `--no-engine` changes nothing, and no variable or Engine VM note is added.

Containment, set from outside the VM, since the agent has root inside it:

- **No route out.** The VM uses QEMU's user-mode networking with `restrict=on`, so the guest can't open connections to the internet, DNS servers, the host or the LAN, even as root with its own routes. It never has a route out, not even during prep. Images come in only as the archives prep downloaded.
- **Only the agent's ports.** QEMU forwards only the Docker API and the published port range (40000-40099, set as the VM's `ip_local_port_range`, so Docker publishes container ports only there), each from a free port on the host's `127.0.0.1`. An engine forwarder container, like the gateway's, relays them to the sandbox's internal network as `engine`, with the VM's own port numbers.
- **Only this session's client.** The forwards listen on the host's loopback, which any container can reach, so dockerd requires TLS with a client certificate. Each session makes a new CA, server and client certificate (EC P-256) in its session directory. The server side reaches the VM through QEMU's `fw_cfg` and is read at boot, so the shared disk holds no keys. Only the client side is mounted, read-only, into the sandbox, and the probes check that it rejects writes. A certificate from another session or project, or none, is refused.
- **The route dies with the VM.** If the VM stops during a session (the agent can power it off as root), kota kills the engine forwarder at once, so the agent can't reach whatever binds the freed host ports next.
- **Limit: bursts of new connections.** QEMU's port forwards accept only a few pending connections at a time, so in a burst of parallel new connections to one port some are cut off during the TLS handshake (8 of 40 in a test). Sequential connections and kept-alive ones aren't affected. The probes run their API checks one at a time for this reason.
- **Limit: published ports have no authentication.** Like the API, the published-port forwards listen on the host's `127.0.0.1`, which other containers can reach, for example another project's `prep`. Only the API requires the certificate; a container the agent publishes (a test database) is as open to them as its own credentials make it.
- **Nothing carries over.** Every session boots from the **Engine base disk** through `-snapshot`, a throwaway layer, so files, containers and images from the last session are gone. The VM has no host directories mounted.
- **The sandbox stays as it was.** The agent container is still unprivileged with all capabilities dropped, on the internal network only, and without the host's Docker socket.

The VM boots its kernel directly, without firmware or a boot loader. With `postgres:16-alpine` it is ready in about 5 seconds, booting while the sandbox starts. It has the `cpus` and `memory` of the engine section, 2 CPUs and 2 GB by default.

`kota build` also builds the Engine base disk, once, in `kota/engine/base/` under the user cache dir. It downloads the Debian 13 cloud image and checks it against its pinned SHA-512, then runs one online boot that installs Docker from Debian, sets up the port range and the TLS service, disables cloud-init and powers off (about 1.5 minutes). That boot gets its cloud-init seed from a server on the host's loopback, which also receives the kernel, initrd and root partition for the direct boot. It runs no project code and isn't any project's Engine VM. Later builds skip it while it is up to date; sessions warn when the disk is from an older definition. Without QEMU, `build` skips the disk with a note.

Before every `run` and `shell` with the Engine VM, after the sandbox's probes, the VM is probed too:

- From a probe container inside the VM, on the VM's own network, so with everything root in the VM could reach: the internet over IPv4 and IPv6, DNS (the VM's resolver, QEMU's DNS relay `10.0.2.3` and `1.1.1.1`), and the host through QEMU's host alias `10.0.2.2`, the host's own addresses and the LAN gateway, on the gateway's port, the usual service ports and the host side of the VM's own forwards. All must fail. A connection to the VM itself must succeed, so a broken probe container can't pass for a contained VM. A must-fail probe whose command is missing counts as failed, here and in the sandbox's probes.
- From the sandbox: the Docker API must answer with the session's certificate and refuse a client without one, and a port published by a probe container in the VM must answer through the engine route.

The VM can't download anything, so the probe image is the forwarder image (alpine with socat), saved from the host's Docker and loaded for the probes only; it and the probe container are removed afterwards. A failed probe blocks the session like a sandbox probe.

A `run` or `shell` of an engine project refuses to start, with the fix, when QEMU isn't installed, the Engine base disk isn't built (`kota build`), a declared image hasn't been prepped (`kota prep`), the VM doesn't boot to a ready Docker API within 60 seconds, or an Engine VM probe fails. `--no-engine` starts it anyway, without the VM; `--unsafe-skip-check` skips the Engine VM probes along with the sandbox's. If `kota.yaml` can't be parsed, a project that was prepped with an engine refuses too, since its images aren't known; other projects start as before.

All engine data lives under `kota/` in the user cache dir, never in the project: the shared Engine base disk in `engine/base/`, and each project's image archives in `engine/projects/<hash>/`, next to the running VM's pidfile. A VM leaves no disk behind: `-snapshot` keeps its writes in a temporary file QEMU deletes. Its certificates and console log are in the session directory under `sessions/`, removed when the session ends. `kota clean [dir]` lists the project's engine directory with its archived images, and any session directories left by sessions that didn't end cleanly (they may still hold certificates), and removes them with the volumes after you type `y`. The next `prep` then downloads the images again. The shared base disk is kept; `kota build` replaces it when its definition changes.

Requirements: QEMU (`brew install qemu` on macOS; it ships the UEFI firmware the base disk build uses). The setup uses only QEMU features that Linux has too, with KVM instead of macOS's HVF, but it is so far only tested on macOS on Apple silicon.
