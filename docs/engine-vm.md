# Engine VM

Some projects' tests start containers, for example testcontainers with Postgres, and some apps need a service such as a database to run. The sandbox has no Docker of its own, and never gets the host's: controlling a Docker engine is root on the machine that runs it. A project that needs containers declares the images in `kota.yaml` instead, and gets its own **Engine VM**: a small QEMU VM, managed by kota, that runs only the containers started in its sessions, with no route out. Why it is a VM is in [ADR 0001](adr/0001-engine-vm-for-agent-containers.md).

## Setting it up

1. Install QEMU (`brew install qemu` on macOS).
2. Run `kota build`. Besides the images, it builds the **Engine base disk** every Engine VM boots from (about 1.5 minutes, once).
3. Declare the images the project's tests use in `kota.yaml`:

   ```yaml
   engine:
     images: [postgres:16-alpine]   # plain references with an optional tag
     cpus: 2                        # optional, default 2
     memory: 2G                     # optional, default 2G; M or G (MiB or GiB), at least 512M
   ```

4. Run `kota prep`. It downloads the images, after asking you to confirm them.

`images` lists each image once, with a tag or none (`latest`), never a digest: `prep` records the digest the tag resolves to. `cpus` is a whole number of at least 1. The VM runs next to your model, so keep it small. An invalid entry is rejected with a message naming it, by `prep`, and by `run` and `shell` once the project has been prepped with an engine. `kota.yaml` is read-only in the sandbox, so the agent can't declare images or resize the VM.

Projects without an `engine:` section start no VM.

## In a session

`kota run` and `kota shell` start the project's Engine VM, for opencode and pi alike, load the images, and point the sandbox at it:

- `DOCKER_HOST=tcp://engine:2376`, `DOCKER_TLS_VERIFY=1` and `DOCKER_CERT_PATH=/etc/kota/engine`, so the Docker CLI and testcontainers use it;
- `TESTCONTAINERS_HOST_OVERRIDE=engine`, so tests reach their containers' published ports through the same route;
- `TESTCONTAINERS_RYUK_DISABLED=true`. Ryuk would need the Docker socket mounted into a container, and the VM is reset every session anyway.

The tests need no changes. With `postgres:16-alpine` the VM is ready in about 5 seconds, booting while the sandbox starts. It stops when the session ends, including on Ctrl-C, SIGTERM or a closed terminal; if kota itself is killed outright, the next command for the project stops it. `kota check` starts it too, lists the declared images with the digests `prep` recorded, and runs its probes.

**Only declared images work.** The VM never downloads an image. A test or `docker` command that needs an image not in `engine.images` fails within seconds with Docker's own error, a failed lookup of the registry (`lookup registry-1.docker.io: Temporary failure in name resolution`). The Docker CLI names the image first (`Unable to find image 'redis:7' locally`); testcontainers' error doesn't. Add the image to `kota.yaml` and run `kota prep`.

**The agent is told** whether it has the Engine VM, so it doesn't claim its container tests passed when they couldn't run. In an engine project, `KOTA_ENGINE` is `available` or `unavailable`, and a note is added to the agent's instructions (see [What the agent is told](agent-setup.md#what-the-agent-is-told)). It says how a container the agent starts is reached (below), and that a refused connection at `localhost` or outside the port range means the route, not the service. It lists the declared images, explains what a failed pull means, and tells the agent that the test keeps needing that image: it leaves the image the test uses and its container in place, reports the missing image as the cause, names the tests that couldn't run, and asks you to add the image.

**Containers the agent starts**, such as a database for running the app, are reached the way testcontainers reaches its own: at host `engine`, on a port published in 40000-40099, one per container (`docker run -d -p 40000:5432 postgres:16-alpine`, then `engine:40000`), or on the free port in that range Docker picks for `-p 5432`. A port published outside the range, such as `-p 5432:5432`, or bound to `127.0.0.1`, works inside the VM but can't be reached from the sandbox. Containers on a Docker network in the VM reach each other by name on their usual ports. The VM starts empty every session, so the agent starts its containers and loads their data each time. The base image has the Docker CLI without Compose; a `kota.Dockerfile` can add it.

**`--no-engine`** on `run` or `shell` starts the session without the VM on purpose, for work that doesn't need containers, or when the VM can't start. Startup says so, and the agent is told that tests needing containers can't run and must be reported as not run, never as passed. In a project without an `engine:` section, it changes nothing.

## When it won't start

A `run` or `shell` of an engine project refuses to start, and says how to fix it, when:

- QEMU isn't installed;
- the Engine base disk isn't built (`kota build`);
- a declared image hasn't been prepped (`kota prep`);
- the VM doesn't have a ready Docker API within 60 seconds;
- an Engine VM probe fails;
- `kota.yaml` can't be parsed, in a project prepped with an engine, since its images aren't known.

`--no-engine` starts the session anyway, without the VM. `--unsafe-skip-check` skips the Engine VM probes along with the sandbox's.

## Containment

The agent has root inside the VM, so its containment is set from outside it:

- **No route out.** The VM uses QEMU's user-mode networking with `restrict=on`, so the guest can't open connections to the internet, DNS servers, the host or the LAN, even as root with its own routes. It never has a route out, not even during `prep`. Images come in only as the archives `prep` downloaded.
- **Only the agent's ports.** QEMU forwards only the Docker API and the published port range (40000-40099, set as the VM's `ip_local_port_range`, so Docker publishes container ports only there), each from a free port on the host's `127.0.0.1`. An engine forwarder container, like the gateway's, relays them to the sandbox's internal network as `engine`. Between the two, kota relays each port on the host's `127.0.0.1` for the session: QEMU listens with a backlog of one, so of several connections that arrive together (as Node's testcontainers opens them) most would be reset. The relay redials a connection reset before the VM sent anything, and sends again what the client had sent.
- **Only this session's client.** The forwards listen on the host's loopback, which any container can reach, so dockerd requires TLS with a client certificate. Each session makes a new CA, server and client certificate (EC P-256). The server side reaches the VM through QEMU's `fw_cfg`, so the shared disk holds no keys. Only the client side is mounted, read-only, into the sandbox. A certificate from another session or project, or none, is refused.
- **The route dies with the VM.** If the VM stops during a session (the agent can power it off as root), kota closes the relays and kills the engine forwarder at once, so the agent can't reach whatever binds the freed host ports next.
- **Nothing carries over.** Every session boots from the Engine base disk through a throwaway layer (`-snapshot`), so files, containers and images from the last session are gone. The VM has no host directories mounted.
- **The sandbox stays as it was.** The agent container is still unprivileged with all capabilities dropped, on the internal network only, and without the host's Docker socket.

### Probes

Before every `run` and `shell` with the Engine VM, after the sandbox's probes, the VM is probed too:

- **From inside the VM**, in a probe container on the VM's own network, so with everything root in the VM could reach: the internet over IPv4 and IPv6, DNS (the VM's resolver, QEMU's DNS relay `10.0.2.3` and `1.1.1.1`), the cloud metadata address `169.254.169.254`, and the host through QEMU's host alias `10.0.2.2`, the host's own addresses and the LAN gateway, on the gateway's port, the usual service ports and the host side of the VM's own forwards and of kota's relays. All must fail. A connection to the VM itself must succeed, so a broken probe container can't pass for a contained VM, and a must-fail probe whose command is missing counts as failed.
- **From the sandbox**: the Docker API must answer with the session's certificate and refuse a client without one, and a port published by a probe container in the VM must answer through the engine route.

The VM can't download anything, so the probe image is the forwarder image (alpine with socat), saved from the host's Docker and loaded for the probes only, then removed. A failed probe blocks the session like a sandbox probe.

## Limits

- **Published ports have no authentication.** Like the API, the published-port forwards listen on the host's `127.0.0.1`, which other containers can reach, for example another project's `prep`. Only the API requires the certificate; a container the agent publishes (a test database) is as open to them as its own credentials make it.
- **Bursts of new connections** hold only through kota's relay. It redials a reset connection for up to 2 seconds, and only while the client has sent at most 64 KiB with no answer yet; past either, the reset reaches the client. In tests, 50 parallel API calls over TLS and 20 parallel Postgres clients all connected.
- **Tested only on macOS** on Apple silicon. The setup uses only QEMU features Linux has too, with KVM instead of macOS's HVF.

## Details

### Downloading images

`kota prep` downloads the images with crane, after the dependency fetch and with no project code running: crane runs in a throwaway container of the base image with only the project's new archive directory mounted, not the project. Each tag is resolved to its digest and the image pulled by that digest, so every manifest and layer is checked against it.

Each later `prep` first looks up which digest each tag of the last `prep` points to now. It looks up only the images the last `prep` recorded: an image newly declared in `kota.yaml`, which the agent may have written, isn't sent anywhere until you confirm. When the images changed, even if no file did, `prep` lists them and asks you to type `y`:

```
Engine images, downloaded with crane (no project code runs):
  new      redis:7
  moved    postgres:16-alpine: sha256:3f5a9c1e2b7d -> sha256:9b04e8d2c6a1
  removed  nginx:1.27
```

Only new images, images whose tag moved (pulled by the digest shown) and images whose archive is gone are downloaded. A failed lookup or download names the image and keeps the previous archives.

### The base disk

`kota build` builds the Engine base disk once, in `kota/engine/base/` under the user cache dir. It downloads the Debian 13 cloud image and checks it against its pinned SHA-512, then runs one online boot that installs Docker from Debian, sets up the port range and the TLS service, disables cloud-init and powers off. That boot gets its cloud-init seed from a server on the host's loopback, which also receives the kernel, initrd and root partition, so sessions boot the kernel directly, without firmware or a boot loader. It runs no project code and isn't any project's Engine VM. Later builds skip it while it is up to date; sessions warn when the disk is from an older definition. Without QEMU, `build` skips the disk with a note.

### Where data lives

All engine data lives under `kota/` in the user cache dir, never in the project: the shared base disk in `engine/base/`, and each project's image archives and recorded digests in `engine/projects/<hash of the project path>/`, next to the running VM's pidfile. A VM leaves no disk behind: `-snapshot` keeps its writes in a temporary file QEMU deletes. Its certificates and console log are in the session directory under `sessions/`, removed when the session ends.

`kota clean [dir]` removes the project's engine directory, and any session directories left by sessions that didn't end cleanly (they may still hold certificates). The next `prep` then downloads the images again. The shared base disk is kept; `kota build` replaces it when its definition changes.
