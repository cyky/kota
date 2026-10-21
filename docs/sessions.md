# Sessions

A **Session** is one `kota run` (an agent) or `kota shell` (Bash) in the sandbox, for one project. This page covers what happens in one, the options that change it, and how `clean` removes what sessions leave behind. Why each part is there is in [The sandbox](sandbox.md).

## What happens in a session

1. kota checks the project path, takes the project's lease (one command per project at a time), and asks any confirmations, before anything is written or started.
2. It reads the model list from the gateway and renders the agent's config.
3. It starts the containers: the agent's, on a network whose only way out is a forwarder to the gateway, and the project's [Engine VM](engine-vm.md) if it has one.
4. It runs the containment probes. Any failure stops the session here.
5. The agent (or Bash) runs in `/workspace`, as your UID.
6. When it exits, kota tears everything down and prints the change report: files that can run code on your machine, and commits (see [Protecting the host](protections.md)).

## Approval

By default opencode asks before it edits a file or runs a shell command. `--auto` approves both, so it works without interruption; in a non-interactive `opencode run`, a request that needs approval is rejected. pi has no permission system: it always acts without asking, and `run --agent pi` says so at startup. Either way, the sandbox is then the only boundary.

Before a full-auto session (pi, or opencode with `--auto`) in a git project with uncommitted changes, `run` lists them and asks you to type `y`. Anything else aborts before a container starts, so you can commit or stash first, and the agent's work doesn't mix with yours. Non-git projects aren't asked.

The check doesn't run git on the host, where a filter or fsmonitor the agent configured (in a repository it created, or through `.gitattributes`) would run. It runs `git status` in a throwaway container of the base image, offline, without capabilities, with only the project and its git directory mounted read-only. For a linked worktree or submodule checkout, that includes the git dir its `.git` file points to, which must pass the same path rules as `--ro`.

## Models

Models come from the gateway: at launch, kota reads its `/v1/models`, so a model added to llama-swap shows up in the agent without editing anything. `--model ID` picks another default for one session; an ID the gateway doesn't serve is refused with the list of available ones. `kota check` shows the list.

A models file adds what the gateway doesn't report: each model's context window, maximum output and tool-call support, `agent: false` to hide a model, the default model and the small model opencode uses for titles (pi has none). Models not listed there get the values of its `unknown` entry. The built-in one is `assets/models.yaml`: its default is `qwen3.6-coder`, its small model `qwen3.5-2b-instruct`, and it hides `zeta2.1`, a completion model.

Your `config.yaml` (see [Gateway](#gateway)) can set models in the same format, so you don't have to reinstall kota to change them: under a gateway for that gateway only, or at the top level as defaults for the gateways without their own, and for a gateway given as a URL.

```yaml
models:                      # defaults
  default: qwen3.8-27b
  unknown: { context: 8192, output: 2048, tools: true }
  models:
    qwen3.8-27b: { context: 131072, output: 16384, tools: true }
gateways:
  local:
    url: http://host.docker.internal:8686           # uses the defaults above
  work-api:
    url: http://host.docker.internal:4000
    models:                                          # only these
      default: big-coder
      unknown: { context: 32768, output: 8192, tools: true }
      models:
        big-coder: { context: 200000, output: 32000, tools: true }
```

One set applies as a whole: the gateway's own models, else the config's defaults, else the built-in ones. Nothing is merged between them, so a gateway with its own models gets none of the defaults' entries, and the defaults none of the built-in ones; gateways often serve entirely different models. Each section needs `default` and an `unknown` with `context`, `output` and `tools`, as `assets/models.yaml` does, and a missing one is an error that names the section. `check` shows which set applies (`model settings:`).

When a session starts with a different model than another running session on the same gateway, kota warns that a gateway that loads one model at a time, such as llama-swap, will swap models back and forth. It is only a warning: the session starts anyway. Sessions on different gateways are never compared. It compares the models chosen at startup only; a model changed later inside the agent isn't seen. `shell`, `prep`, `check` and `clean` don't count as model users.

## Shell

`kota shell [dir]` opens interactive Bash in `/workspace`, without an agent, in the same sandbox as `run`: the same image, mounts, masks, dependency volumes, gateway access and probes. The change report runs when you exit. Startup skips Bash profile and rc files.

`--agent pi` selects pi's config and state volume (opencode is the default). `--model`, `--gateway`, `--ro`, `--no-engine` and `--no-history` work as for `run`; `--auto`, `--unsafe-skip-check` and a command after `--` don't. The gateway must be available, since the agent's config is rendered as for `run`.

## Project and `--ro` paths

The project must be inside the allowed root, `~/Projects` by default (`KOTA_ALLOWED_ROOT`). Paths are resolved through symlinks first, and `$HOME`, `/` and the allowed root itself are refused.

`--ro PATH` (repeatable) mounts a reference directory read-only at `/ro/<name>`. It can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories or Docker socket locations. `--ro` paths aren't masked, so don't point it at secrets.

## Gateway

Without any setting, `run`, `shell` and `check` use llama-swap on the host at `http://host.docker.internal:8686`. To work with more than one gateway, define each under a name in `~/.config/kota/config.yaml` (`$XDG_CONFIG_HOME/kota/config.yaml` when that is set to an absolute path):

```yaml
defaultGateway: local
gateways:
  local:
    url: http://host.docker.internal:8686
  work-api:
    url: http://host.docker.internal:4000
```

Then pick one per command with `--gateway work-api`, or with `KOTA_GATEWAY=work-api`. Both also take a URL, which is used as given. The first of these that is set wins: `--gateway`, `KOTA_GATEWAY`, the config's `defaultGateway`, then the built-in default. A name the config doesn't define is refused with the list of names it does. A gateway URL is plain `http://` with a host and an optional port, and no path.

`run` and `shell` print the gateway they use when they start, and `check` shows it with where the choice came from. The config is yours, not the project's: `kota.yaml` can't choose a gateway, so a repository you clone can't send your sessions somewhere else ([ADR 0002](adr/0002-gateways-defined-in-user-config.md)). Only `run`, `shell` and `check` read the config, and they check all of it: an unknown key (with its line), a gateway without a valid `url`, or a `defaultGateway` that isn't defined stops them, even when the mistake is in a gateway you didn't pick. `build`, `prep`, `clean` and `instructions` don't read it.

## Gateway key

If the gateway needs an API key (llama-swap with `apiKeys` enabled, for example), where kota takes it from depends on how the gateway was chosen:

- **A named gateway** uses only its own `keyFile`, a file that holds the key. The config holds the path, never the key, so it can go in a dotfiles repository:

  ```yaml
  gateways:
    work-api:
      url: http://host.docker.internal:4000
      keyFile: ~/.config/kota/keys/work-api
  ```

  The path is absolute or starts with `~/`. `KOTA_GATEWAY_KEY` and `KOTA_GATEWAY_KEY_FILE` are ignored for a named gateway, so a key in your shell never reaches a gateway it wasn't meant for. Only the chosen gateway's file is read: a missing key file for another gateway doesn't stop a session.
- **A gateway given as a URL**, or the built-in default, uses `KOTA_GATEWAY_KEY`, or `KOTA_GATEWAY_KEY_FILE` naming a file that holds the key.

Keep key files outside your projects. Surrounding whitespace is trimmed, and the key must be printable ASCII without spaces. Without a key, nothing changes. If the gateway answers 401, `run` and `check` say so and how to set the key for that gateway. `check` shows where the key came from (`keyFile <path>`, `KOTA_GATEWAY_KEY`, `KOTA_GATEWAY_KEY_FILE <path>`, or `none`), never its value.

The agent can read the key: inside the sandbox it is `$KOTA_GATEWAY_KEY`, which the agent configs refer to by name. So use a key meant only for the gateway. It is passed to `docker compose` in the environment only, never written to disk or put on a command line. `prep` never gets it, so install scripts can't use your models.

## Network

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway, which is `http://gateway:8080` inside the sandbox. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

## Agent config

The agent's config is versioned in this repo and rendered for each session with the gateway provider and models, then mounted read-only. Your host's agent config (`~/.config/opencode`, `~/.pi`) is never mounted, and the agent's `HOME` is an empty tmpfs thrown away after the session.

The project can't change the agent's config either, since the agent could write it there for a later session:

- opencode gets its config at `/etc/kota/opencode.json`, and ignores the project's `opencode.json` and `.opencode/` directory, which could change its own permissions.
- pi gets `settings.json` and `models.json` in its config directory `~/.pi/agent`, its own tmpfs (writable because pi creates lock files there). The settings set `defaultProjectTrust` to `never`, so pi doesn't load the project's `.pi/` (settings, extensions, skills, system prompt), yours included. pi's sessions are stored in its state volume through `PI_CODING_AGENT_SESSION_DIR`, which a `sessionDir` in the project can't override.

`AGENTS.md` and other context files in the project still load. What kota tells the agents, and your own preferences for them, are in [The agents](agent-setup.md).

## Agent state

Each agent has a Docker volume per project for its sessions, logs and caches, `kota-state-<agent>-<hash of the project path>`, mounted at `/var/lib/kota/state`. A later session of the same project can resume its conversations (`kota run [dir] -- opencode -c`, `-- pi -c`), and other projects can't see them. Tool caches such as Go's build cache live there too. `--no-history` sessions have their own volume, so transcripts of normal sessions, which may hold git history, don't reach them. Volumes stay until `kota clean [dir]`.

If the "state writable" probe fails after upgrading kota, run `kota build` and remove the project's state volume, which an older image created owned by root.

## Containment probes

Before every `run` and `shell`, probes run in a fresh agent container. They must fail to reach the internet over IPv4 and IPv6, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway. They must succeed in reaching the gateway and writing `/workspace` and the state volume, and every read-only mount (`--ro` paths, the agent config, protected git files) must reject writes, and every masked secret must read as empty. An [Engine VM](engine-vm.md) has its own probes after these.

Any failure stops the session. `--unsafe-skip-check` starts it anyway, with a warning. `kota check` runs the probes and shows the mounts and masks without starting an agent.

## One command per project

Only one `run`, `shell`, `prep`, `check` or `clean` can use a project at a time. A second is refused with the operation, agent, host PID and start time of the one running. Symlinked paths to the same project count as the same project. Different projects can run at the same time.

The lease is held from the path checks, through confirmations, startup and teardown, to the end of the change report. It is an OS file lock in `kota/registry/` under the user cache dir, so it is released when kota exits, however it exits. The next command for a project whose kota died removes its leftover containers first; if that fails, it refuses to start and tries again next time. Don't delete the lock files, even when no session is running.

## Interrupts

Ctrl-C, `kill` (SIGTERM) or a closed terminal (SIGHUP) leaves nothing behind. Confirmations come before anything is written or started, and Ctrl-C at a prompt counts as no. Once a session is starting, a signal stops it at the next step: the agent isn't started, and the containers, networks, placeholders and session directory are removed as on an error. The change report still runs, and kota exits with status 130. Inside the agent's terminal, Ctrl-C goes to the agent as usual.

Only a process killed outright (SIGKILL, a crash) skips this. The next command for the project then removes its containers and unlocks the files it [locked on the host](protections.md#locked-files-on-the-host), but doesn't remove the placeholders and seeded files it left in the project (`git status --ignored` shows them) or its session directory.

## Cleanup

`kota clean [dir]` lists what kota keeps for the project, and removes it after you type `y`:

- both agents' state volumes, and its dependency volumes, including those of directories the configuration no longer mentions;
- the prep record, so the next `prep` asks for approval again;
- its Engine VM data, including the downloaded images (see [Engine VM](engine-vm.md));
- copies kept after `--no-history` sessions, with any work in them that wasn't brought back.

Project images, source files and the shared caches are kept. Afterwards, run `prep` again to restore the dependencies. `clean` needs no gateway and doesn't parse the project's configuration, so it works when the configuration is broken or gone. It refuses while another command is using the project.

`kota clean --shared-caches [dir]` also resets the caches all projects share: the Go module cache, corepack's releases and the pnpm store. It needs every session of every project to be idle, and blocks new ones until it is done. Other projects then need `prep` again to refill them.

If listing or removal fails, `clean` reports it and keeps the prep record. A removal can fail after some volumes are already gone, and Docker refuses to remove a volume a container still uses.
