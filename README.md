# kota

Run coding agents (opencode, pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

A kota is a Finnish (and Sámi) hut: a simple shelter where the agent works, apart from everything else.

Status: early, a personal project built for one setup: macOS with Rancher Desktop on Apple silicon, and local models behind llama-swap. kota reduces the risk of letting an agent work unattended; it isn't a guarantee. Read [the limits](docs/sandbox.md#drawbacks-and-limits) before you point it at a project with secrets.

## Requirements

- Go 1.27 or later, to install kota
- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway that speaks the OpenAI API, by default llama-swap on the host on port 8686. It can also run in a container on a Docker network ([Sessions](docs/sessions.md#a-gateway-in-a-container)). See [Models](#models) if yours serves other models.
- QEMU, only for projects with an [Engine VM](docs/engine-vm.md)

## Quick start

```sh
git clone https://github.com/cyky/kota.git && cd kota
go install ./cmd/kota
kota build              # build the sandbox images, once and after updating kota
cd ~/Projects/app
kota prep               # install the project's dependencies (online, no agent)
kota check              # check the sandbox is contained, without starting an agent
kota run                # start opencode; it asks before it edits files or runs commands
```

The agent sees only the project, at `/workspace`, with secrets such as `.env` masked. It has no network except the model gateway, so it can't install anything: when it needs a new dependency, add it to the manifest on the host and run `kota prep` again.

When the session ends, kota lists files the agent changed that could run code on your machine (`package.json` scripts, `Makefile`, CI workflows, ...) and the commits it made. Review them before you build or open the project.

## Commands

```sh
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
kota run --skills-writable [dir]   # let the agent edit .agents/skills (also for shell)
kota check [dir]                   # show mounts, the Engine VM and run the containment probes
kota clean [dir]                   # remove project state/dependency volumes and engine data after confirmation
kota clean --shared-caches [dir]   # also reset shared caches; requires all sessions idle
kota instructions                  # print the agent's working style and whether it's your override
```

`kota help` shows every flag.

## Configuration

| Variable | What it sets | Default |
|---|---|---|
| `KOTA_GATEWAY` (or `--gateway NAME\|URL`) | The model gateway: a URL, or the name of one defined in `~/.config/kota/config.yaml` ([Sessions](docs/sessions.md#gateway)), which can be a gateway in a container on a Docker network ([Sessions](docs/sessions.md#a-gateway-in-a-container)) | the config's `defaultGateway`, else llama-swap on the host at `http://host.docker.internal:8686` |
| `KOTA_GATEWAY_KEY` or `KOTA_GATEWAY_KEY_FILE` | The API key of a gateway given as a URL, if it needs one (a named gateway uses its `keyFile` instead; see [Sessions](docs/sessions.md#gateway-key)). The agent can read it, so use a key meant only for the gateway. | none |
| `KOTA_ALLOWED_ROOT` | The directory projects must be inside | `~/Projects` |

### Models

The agent can use every model the gateway lists. [`assets/models.yaml`](assets/models.yaml) adds what the gateway doesn't report, such as each model's context window, and names the default model, `qwen3.6-coder`. A session refuses to start when the gateway doesn't serve the default, and lists the models it does. Pass `--model ID`, or set your own default and context sizes in `~/.config/kota/config.yaml`, for every gateway or for one, without reinstalling kota. Details: [Sessions](docs/sessions.md#models).

### Your own settings

Your own working style for the agent, and agent settings such as keybindings, go in `~/.config/kota/`; see [The agents](docs/agent-setup.md). So do your named gateways, in `config.yaml` there; see [Sessions](docs/sessions.md#gateway). A project's own settings go in `kota.yaml`, `.kotaignore` and `kota.Dockerfile` at its root; see [Dependencies](docs/dependencies.md) and [Protecting the host](docs/protections.md). `skills: true` in `kota.yaml` loads the project's agent skills from `.agents/skills`, read-only; see [Project skills](docs/sessions.md#project-skills).

## How it works

kota runs the agent in a container on an internal Docker network whose only way out is a forwarder to your model gateway. Before every session it probes that containment. It masks secrets, keeps git hooks and config read-only, and afterwards reports changed files and commits that could run code on your machine. What this protects against, how, and what it doesn't, is in [The sandbox](docs/sandbox.md).

## Documentation

- [The sandbox](docs/sandbox.md): the threat model, what the agent can and can't do, and the limits and risks
- [Sessions](docs/sessions.md): `shell`, paths, the gateway key, network, approval, models, agent config, Project skills and state, probes and cleanup
- [Protecting the host](docs/protections.md): git hook and config protection, secret masks and the change report
- [The agents](docs/agent-setup.md): what the agent is told, your own agent settings, and pi's `ticket_done`
- [Sessions without git history](docs/no-history.md): `--no-history`, and what it doesn't hide
- [Dependencies](docs/dependencies.md): `prep`, shared caches and project images
- [Engine VM](docs/engine-vm.md): containers for a project's tests
- [Architecture](docs/architecture.md): diagrams of the runtime, the command flows, the packages and where data lives
- [Development](docs/development.md): the layout, building and running kota's tests
- [Design notes and issues](.scratch/README.md): every feature's PRD and issues, done and open

## License

[MIT](LICENSE)
