# kota

Run coding agents (opencode, pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

A kota is a Finnish (and Sámi) hut: a simple shelter where the agent works, apart from everything else.

Status: early. Network containment, secret masking and git-hook protection are verified before every session, and changed files that run things are reported after it. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8686
- QEMU, only for projects with an [Engine VM](docs/engine-vm.md)

## Usage

```sh
go install ./cmd/kota
kota build                         # build the sandbox images and the Engine base disk
kota build [dir]                   # build dir's image from its kota.Dockerfile
kota prep [dir]                    # install dependencies (network, no agent)
kota run [dir]                     # opencode in dir (default: current dir)
kota run [dir] -- opencode run "…" # any command inside the sandbox
kota run --ro ../specs [dir]       # also mount ../specs read-only at /ro/specs
kota run [dir] -- opencode -c      # continue the project's last opencode session
kota run --model qwen3.5-2b-instruct [dir]  # start with another model
kota run --auto [dir]              # opencode acts without asking
kota run --agent pi [dir]          # pi instead of opencode (always full auto)
kota run --agent pi [dir] -- pi -c # continue the project's last pi session
kota shell [dir]                   # interactive Bash in the same offline sandbox
kota shell --ro ../specs [dir]     # shell with a read-only reference mount
kota run --no-engine [dir]         # without the project's Engine VM (also for shell)
kota run --no-history [dir]        # on a copy that keeps only the last commit (also for shell)
kota check [dir]                   # show mounts, the Engine VM and run the containment probes
kota clean [dir]                   # remove project state/dependency volumes and engine data after confirmation
kota clean --shared-caches [dir]   # also reset shared caches; requires all sessions idle
kota instructions                  # print the agent's working style and whether it's your override
```

`kota shell [dir]` opens interactive Bash in `/workspace` as your UID, without starting an agent. It shares `run`'s project image, mounts, secret masks, dependency volumes, gateway access and containment probes. The probes must pass before the shell opens. Use `--agent pi` to select pi's config and state volume (opencode is the default); `--model`, `--gateway`, `--ro`, `--no-engine` and `--no-history` work as for `run`. Agent configuration is rendered from the gateway's model list as usual, so the gateway must be available. Shell startup skips Bash profile and rc files. On exit, changed files that can run code on the host are reported, as for `run`. `--auto`, `--unsafe-skip-check` and command overrides are only available on `run`.

Set the gateway with `--gateway URL` or `KOTA_GATEWAY` (default `http://host.docker.internal:8686`).

If llama-swap has `apiKeys` enabled, give kota the key with `KOTA_GATEWAY_KEY`, or with `KOTA_GATEWAY_KEY_FILE` pointing to a file that contains it (keep the file outside your projects; surrounding whitespace is trimmed). Without either, nothing changes. The key is handed to `docker compose` in its environment only: the compose file names the variable without a value, so the key isn't written to disk or put on a command line. Inside the sandbox it is `$KOTA_GATEWAY_KEY`, which the agent configs refer to by name (`{env:...}` for opencode, `$...` for pi), and the model list fetch and gateway probe send it as `Authorization: Bearer`. The agent can read it, so use a key meant only for the gateway. `check` shows whether a key is configured, never its value. If the gateway answers 401, `run` and `check` say so and point to these variables. `prep` never gets the key, so install scripts can't use your models through it.

The project must be inside the allowed root, `~/Projects` by default (set `KOTA_ALLOWED_ROOT` to change it). Paths are resolved through symlinks first. `$HOME`, `/` and the allowed root itself are refused. `--ro PATH` (repeatable) can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories, or Docker socket locations. Each `--ro` path is mounted at `/ro/<name>`.

## How it works

kota runs the agent in a container on an internal Docker network whose only way out is a forwarder to your model gateway. Before every session it probes that containment. It masks secrets, keeps git hooks and config read-only, and afterwards reports changed files and commits that could run code on your machine. What this protects against, how, and what it doesn't, is in [The sandbox](docs/sandbox.md).

## Documentation

- [The sandbox](docs/sandbox.md): the threat model, what the agent can and can't do, and the limits and risks
- [Sessions](docs/sessions.md): network, approval, models, agent config and state, probes and cleanup
- [Protecting the host](docs/protections.md): git hook and config protection, secret masks and the change report
- [The agents](docs/agents.md): what the agent is told, your own agent settings, and pi's `ticket_done`
- [Sessions without git history](docs/no-history.md): `--no-history`, and what it doesn't hide
- [Dependencies](docs/dependencies.md): `prep`, shared caches and project images
- [Engine VM](docs/engine-vm.md): containers for a project's tests
- [Architecture](docs/architecture.md): diagrams of the runtime, the command flows, the packages and where data lives
- [Migrating from localllm](docs/migrating.md)
- [Development](docs/development.md): running kota's tests
