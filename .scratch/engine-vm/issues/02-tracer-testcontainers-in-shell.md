# DONE — 02 — Tracer bullet: testcontainers Postgres passes in `localllm shell`

Status: done

Resolved in: 411c56a

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

The thinnest end-to-end path. A project declares `postgres:16-alpine` under `engine.images` in `localllm.yaml`. `localllm prep` downloads it in the fetch phase, outside the **Engine VM**, with a registry tool, checked against its digest, into a per-project archive cache. `localllm shell` starts the project's **Engine VM** from a clean disk with the setup from issue 01, loads the archive, and points the **Sandbox** at it (`DOCKER_HOST`, `TESTCONTAINERS_HOST_OVERRIDE`, `TESTCONTAINERS_RYUK_DISABLED=true`). Inside, a testcontainers Postgres test passes offline. The VM stops when the shell exits, including on Ctrl-C.

Use the setup the spike proved (issue 01 comments and the PRD's "Engine VM setup"): QEMU run directly, `restrict=on` with forwards on 127.0.0.1 only, `-snapshot` over the **Engine base disk**, a direct kernel boot, and dockerd on TCP with `--tlsverify`. The certificates are per project, passed in through `fw_cfg`, and the client side goes only into the project's **Sandbox**. `localllm build` builds the **Engine base disk** (story 45). Published ports are limited to a fixed range that is forwarded.

The base image gains the Docker CLI and the registry tool. The **Sandbox** container keeps all its hardening, and the host's Docker socket stays unreachable. Projects without an `engine:` section are unchanged. VM names derive from the project hash, so two projects get separate VMs.

## Acceptance criteria

- [x] `localllm build` builds the **Engine base disk** with its kernel and initrd, from a digest-checked cloud image
- [x] The engine API accepts only the project's client certificate
- [x] `prep` downloads declared images into the project's archive cache, with no project code running
- [x] `shell` starts the **Engine VM** from a clean disk with the images loaded, and stops it on exit and on interrupt
- [x] The **Sandbox** environment points Docker and testcontainers at the **Engine VM**, with Ryuk disabled
- [x] Projects without `engine:` start no VM and behave as before
- [x] Unit tests for EngineConfig (minimal), EngineSpec and the SandboxSpec engine additions
- [x] Integration test: a temp project with a testcontainers Postgres test passes offline in `shell` after `prep`
- [x] Integration test: a file written in one **Session**'s **Engine VM** is gone in the next
- [x] README section on the **Engine VM**

## Blocked by

- `01-spike-engine-vm-containment.md`

## Comments

**2026-10-15 — Done.** Verified on macOS (Apple silicon, QEMU 11.1.2 from Homebrew, Rancher Desktop):

- `localllm build` built the Engine base disk in 1m33s from the SHA-512-checked Debian 13 genericcloud arm64 image. The build boot gets its cloud-init seed through QEMU's SMBIOS setting (`ds=nocloud;s=http://10.0.2.2:<port>/<token>/`) from a server on the host's loopback instead of an ISO, and sends back the kernel, initrd and `root=PARTUUID=...` there. That avoids `hdiutil` and works the same on Linux.
- `TestEngineInShell` (integration) passes: prep downloads `postgres:16-alpine` by digest with crane in a container without the project; a shell session boots the VM in about 2.3 s and loads the image in about 2 s while the sandbox starts, all 68 sandbox probes still pass, and a testcontainers-go v0.44 Postgres test with a real query passes offline. The engine accepts only the session's client certificate (none, plain HTTP and another CA's certificate are refused, via curl and the Docker CLI). A file planted in the VM's `/etc` and an extra tag are gone in the next session. QEMU is gone after a session and after SIGINT to `localllm shell`. If the agent powers the VM off mid-session, the engine forwarder is killed, so the freed host ports can't be reached.
- Unit tests cover EngineConfig (minimal), EngineSpec, EngineCerts, the image record and fetch script, the base disk definition, the SandboxSpec engine additions and the shell wiring. The full unit and Docker integration suites pass.

Decisions that differ from the PRD: TLS certificates are made per session in the session directory, not stored per project (nothing to store, and a certificate opens only one session). `run` and `check` don't start the Engine VM yet (issues 04 and 03). Prep downloads every declared image each time (issue 05). Sessions warn when the Engine base disk is from an older definition (story 47). The published range stays 40000-40099, with one socat per port in the engine forwarder.

Security review (manual; the skill needs an `origin` remote): fixed the route staying open to freed host ports after a VM stop, and archives must be regular files. Documented limit: published container ports on the host's loopback have no authentication, so other containers (e.g. another project's prep) can reach them.

Not verified: Linux (issue 09), and an interactive `localllm shell` by hand (the test drives the same startup without a TTY). The base disk is 1.9 GB because `docker.io` pulls in recommended packages; it could be trimmed.
