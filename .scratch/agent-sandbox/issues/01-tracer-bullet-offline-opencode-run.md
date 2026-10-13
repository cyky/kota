# 01 — Tracer bullet: offline opencode session reaching the gateway

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The thinnest end-to-end path: `localllm build` builds the base image, and `localllm run [dir]` starts opencode inside it, offline, able to talk to llama-swap.

- Go CLI skeleton with `build` and `run` subcommands (install via `go install`).
- Base image: Debian slim, git, ripgrep, fd, jq, curl, build-essential, Node LTS with corepack, Go, and a pinned opencode.
- `run` mounts the project (default: current directory) read-write at `/workspace` and starts opencode there.
- Two networks: the agent container sits only on an **internal** network (no route out). A **forwarder** container joins both networks and forwards exactly one port to the gateway (default `http://host.docker.internal:8080`, configurable). The agent reaches the gateway at a fixed in-sandbox hostname (e.g. `gateway`).
- Container runs as the host UID/GID, no sudo, all capabilities dropped, `no-new-privileges`.
- Minimal opencode provider config generated at launch: a `localllm` provider pointing at the in-sandbox gateway URL, default model `qwen3.6-coder`. The full ModelCatalog comes in a later slice.
- The compose spec is produced by a pure **SandboxSpec** builder (session inputs → compose spec) and executed through a thin **DockerDriver**.

No containment probes yet (slice 02). Only use this against throwaway projects until 02 lands.

## Acceptance criteria

- [ ] `localllm build` produces the base image with pinned agent versions
- [ ] `localllm run` in a project starts opencode with `/workspace` = that project; `localllm run <dir>` works too
- [ ] opencode can complete a prompt using `qwen3.6-coder` through the forwarder
- [ ] From inside the agent container, `curl https://1.1.1.1` and `curl http://host.docker.internal:8080` fail; `curl http://gateway:<port>/v1/models` succeeds
- [ ] Files created by the agent are owned by the host user
- [ ] SandboxSpec unit tests: agent attached only to the internal network; forwarder exposes exactly one port; hardening flags present; gateway URL configurable

## Blocked by

None - can start immediately
