# localllm

Run coding agents (opencode, later pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Network containment and git-hook protection are verified before every session, but secret masking is not in place yet. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8080

## Usage

```sh
go install ./cmd/localllm
localllm build                         # build the sandbox images
localllm run [dir]                     # opencode in dir (default: current dir)
localllm run [dir] -- opencode run "…" # any command inside the sandbox
localllm run --ro ../specs [dir]       # also mount ../specs read-only at /ro/specs
localllm check [dir]                   # show mounts and run the containment probes
```

Set the gateway with `--gateway URL` or `LOCALLLM_GATEWAY` (default `http://host.docker.internal:8080`).

The project must be inside the allowed root, `~/Projects` by default (set `LOCALLLM_ALLOWED_ROOT` to change it). Paths are resolved through symlinks first. `$HOME`, `/` and the allowed root itself are refused. `--ro PATH` (repeatable) can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories, or Docker socket locations. Each `--ro` path is mounted at `/ro/<name>`.

## How it works

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

Before every `run`, containment probes run in a fresh agent container. They try to reach the internet, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway and `/workspace` work and that every `--ro` mount rejects writes.

The agent can't plant code that runs later on the host. `.git/hooks`, `.git/config` and `.envrc` are mounted read-only over themselves; a missing `.envrc` or hooks directory is replaced by an empty read-only placeholder, which is removed from the host after the session. A `core.hooksPath` inside the project (husky and similar) is read-only too, as are the hooks, config and `.git` pointer file of each submodule. `.git` itself is mounted over itself so it can't be renamed away and replaced. Git still works inside the sandbox: commit, branch and diff use your host identity, passed in through `GIT_CONFIG_*`. The probes check each of these mounts. Limits:

- In a non-git project there is nothing to protect, so a repository the agent creates with `git init` is not protected.
- In a linked worktree or a submodule checkout opened on its own, `.git` is a file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call, like husky's `.husky/pre-commit`, stay writable. So does an `.envrc` in a subdirectory.
- A protected path that is a symlink is refused, because a read-only mount can't hold it in place. Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

Integration tests need Docker: `go test -tags integration ./...`.
