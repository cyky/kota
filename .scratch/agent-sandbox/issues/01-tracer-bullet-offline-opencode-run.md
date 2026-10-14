# DONE — 01 — Tracer bullet: offline opencode session reaching the gateway

Status: done
Resolved in: `19395a4` (branch `issue-01-tracer-bullet`)

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

- [x] `localllm build` produces the base image with pinned agent versions
- [x] `localllm run` in a project starts opencode with `/workspace` = that project; `localllm run <dir>` works too
- [x] opencode can complete a prompt using `qwen3.6-coder` through the forwarder
- [x] From inside the agent container, `curl https://1.1.1.1` and `curl http://host.docker.internal:8080` fail; `curl http://gateway:<port>/v1/models` succeeds
- [x] Files created by the agent are owned by the host user
- [x] SandboxSpec unit tests: agent attached only to the internal network; forwarder exposes exactly one port; hardening flags present; gateway URL configurable

## Blocked by

None - can start immediately

## Comments

**2026-10-13 — Done.** Implemented in `19395a4` on branch `issue-01-tracer-bullet`.

Verified on the host (Rancher Desktop):
- Internet (`1.1.1.1`) and host (`host.docker.internal`, `192.168.5.2:8080`) unreachable from the agent; `http://gateway:8080/v1/models` reachable.
- `opencode run` with `qwen3.6-coder` made a tool call that created a file on the host, owned by the host user. Tool calling works without `--jinja`.
- Unit tests for SandboxSpec and the opencode config pass.

Not verified: the interactive TUI (`localllm run` with no command). Needs a manual try.

Notes for later issues:
- opencode does not ask before acting by default, so issue 09 must set ask-mode permissions explicitly.
- opencode's npm postinstall must run (it installs the binary), so it's allowed for that package only. pi's `--ignore-scripts` plan (issue 10) is unaffected.

**2026-10-14 — Follow-up fix in `06f8f15`.** Found while working on issue 08: docker compose expands `$VAR` in every compose value from the environment `localllm` runs in. `run -- sh -c '... $op ...'` triggered "variable is not set" warnings and lost the variable. A command mentioning a host variable, such as `$AWS_SECRET_ACCESS_KEY`, would have had the host's value baked in, and env values (the git identity) and paths containing `$` were mangled too. `docker.Start` now writes `Compose.Literal()`, which doubles every `$`. Verified with a unit test and a real run: with `FAKE_HOST_SECRET=leaked` set on the host, `run -- sh -c 'echo [$FAKE_HOST_SECRET] $HOME'` prints `[] /home/agent`.
