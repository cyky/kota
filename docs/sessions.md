# Sessions

How a `run` or `shell` **Session** is set up and torn down, and how `clean` removes what it leaves. What this protects against is in [The sandbox](sandbox.md).

## Network

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

## Approval

By default opencode asks before it edits a file or runs a shell command. `--auto` approves both, so it works without interruption. In a non-interactive `opencode run`, a request that needs approval is rejected. Before a full-auto session in a git project with uncommitted changes (modified, staged or untracked files inside the project), `run` lists them and asks you to type `y`. Anything else aborts before a container starts, so you can commit or stash first. Non-git projects aren't asked. The check doesn't run git on the host, where a filter or fsmonitor that an agent configured (in a repository it created, or through `.gitattributes`) would run. kota finds the repository by looking for `.git` in the project and the directories above it, then runs `git status` in a throwaway container of the base image, with no network, no capabilities and read-only mounts of only the project and its git directory at their host paths. For a linked worktree or submodule checkout, that includes the git dir its `.git` file points to, which must pass the same path rules as `--ro`.

pi has no permission system: it always edits files and runs commands without asking, so the sandbox is its only boundary. `run --agent pi` says so at startup, and the uncommitted-changes confirmation above always applies to it. The containment probes run before pi sessions as for opencode.

## Models

Models come from the gateway. At launch, `run` reads `/v1/models` through the sandbox's forwarder, so a model added to llama-swap shows up in the agent without editing anything. `assets/models.yaml` adds what llama-swap doesn't report:
- the context window, maximum output and tool-call support per model;
- `agent: false` to hide a model (`zeta2.1`, a completion model);
- the default model (`qwen3.6-coder`) and the small model opencode uses for titles (`qwen3.5-2b-instruct`). pi has no small model.

Models not listed there get conservative values from its `unknown` entry. `--model ID` picks another default for one session; an ID the gateway doesn't serve, or a hidden one, is refused with the list of available models. `kota check` shows the resolved list. The generated config is written per session and mounted read-only.

When a new agent session resolves a different model than another active agent session, startup warns that llama-swap will swap models back and forth. The comparison uses the model selected at startup, including `--model`; changes made later inside an agent aren't tracked. Errors recording or scanning models print a warning and the session continues. Registry entries that disappear during a scan are skipped. `shell`, `prep`, `check` and `clean` reserve the project but do not count as model users.

## Agent config

The agent's config is versioned in this repo (`assets/agents/opencode/opencode.json`: permissions and defaults). Each session gets that config plus the gateway provider and model, mounted read-only at `/etc/kota/opencode.json`. Your host `~/.config/opencode`, and any other host agent config, is never mounted. The agent's `HOME` is an empty tmpfs that is discarded when the session ends. opencode ignores the project's `opencode.json` and `.opencode/` directory inside the sandbox, because the agent could write them to change its own permissions. pi's config is versioned the same way (`assets/agents/pi/settings.json`). Each session renders `settings.json` (default model) and `models.json` (the gateway provider and its models), mounted read-only into pi's config directory `~/.pi/agent`. That directory is its own tmpfs, writable because pi creates lock files there, and discarded after the session. The base settings set `defaultProjectTrust` to `never`, so pi doesn't load the project's `.pi/` (settings, extensions, skills, system prompt), which the agent could write for a later session; a `.pi/` of your own isn't used in the sandbox either. `AGENTS.md` and other context files still load. pi's sessions are stored in its state volume via `PI_CODING_AGENT_SESSION_DIR`, which also outranks a `sessionDir` in the project's settings.

Your own preferences for the agents, and what kota tells them, are in [The agents](agent-setup.md).

## Agent state

Agent state (sessions, logs, caches) lives in a Docker volume per agent per project, `kota-state-<agent>-<hash of the project path>`, mounted at `/var/lib/kota/state`. `--no-history` sessions have their own, `kota-state-<agent>-nohistory-<hash>`. So a later `run` of the same project can resume its sessions, and other projects can't see them. The XDG cache dir is in the volume too, so tool caches such as Go's build cache persist per project. Volumes stay until you remove them with `kota clean [dir]`. If the "state writable" probe fails after upgrading, run `kota build` and remove the project's state volume, which an older image created owned by root.

## One command per project

Only one `run`, `shell`, `prep`, `check` or `clean` can use a project at a time. The registry validates the project and read-only mount paths once, locks that resolved project before creating mount placeholders or touching dependency volumes, and uses those same validated paths for preparation. It holds the lease through confirmations, startup, teardown and the change report. A second command is refused with the running operation, agent, host PID and start time. Symlink aliases share the same lock. Different projects can run concurrently.

Registry records live outside the sandbox in `kota/registry/` under the host user cache directory. OS file locks release automatically when a host process dies; the next command for that project removes its orphaned Compose containers (including one-off and stopped containers) before starting. Recovery holds the per-project lock while calling Docker and releases the shared registry guard, allowing other projects to keep using the registry during a slow recovery. If Docker recovery fails, startup is refused and the stale record stays for the next attempt. State/dependency volumes and other projects' containers are preserved. Empty lock files remain after a clean exit; keep them in place, since deleting a lock file while it is in use can allow a second owner.

## Interrupts

Ctrl-C, `kill` (SIGTERM) or a closed terminal (SIGHUP) doesn't leave anything behind. Every confirmation is asked before anything is written to the host or a sandbox container starts, and Ctrl-C at a prompt counts as no. Once a session is starting, a signal stops it at the next step: Docker commands in progress are stopped, the agent is never started after an interrupted startup, and the containers, networks, placeholders and session directory are removed as on an error. The change report still runs, and the lease is released. kota then exits with status 130. Inside the agent's terminal session, Ctrl-C goes to the agent as usual. Only a process killed outright (SIGKILL, a crash) skips this. Recovery then removes its containers, but not its session directory or the placeholders and seeded files it left in the project (`git status --ignored` shows them).

## Containment probes

Before every `run` and `shell`, containment probes run in a fresh agent container. They try to reach the internet over IPv4 and IPv6, external DNS (A and AAAA lookups), the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway, `/workspace` and the state volume work and that every `--ro` mount and the agent config reject writes. For a project with an Engine VM, its own probes follow (see [Engine VM](engine-vm.md)). Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

## Cleanup

`kota clean [dir]` lists the project's existing agent state and dependency volumes, then asks you to type `y`. Declining or reaching end of input leaves them in place. It removes state for both agents and dependency volumes for directories no longer mentioned in the current configuration. The selected volume names must match the exact hash of the resolved project path. It also resets the host's prep confirmation record, so the next `prep` asks for approval again, removes the project's Engine VM data (see [Engine VM](engine-vm.md)) and the copies kept after `--no-history` sessions, with any work in them that wasn't brought back. When there are no volumes, prep record or engine data, it reports that there is nothing to clean.

Cleanup holds the project's session lease through listing, confirmation and removal. It refuses while a run, shell, prep or check is using that project. It works without a model gateway and without parsing the project's dependency configuration, so it can clean up after manifests or configuration have been removed or broken. After cleanup, `run` creates fresh state and empty dependency volumes; run `prep` again to restore dependencies. Project images and source files are preserved.

Shared caches are retained by default. `kota clean --shared-caches [dir]` includes the Go module cache, corepack releases and pnpm store in the confirmed list. It requires exclusive access: any starting, running or finishing session prevents the reset, and a reset in progress prevents new sessions from starting. The cache reservation covers confirmations and gaps between prep containers too. Other projects keep their own state and dependency volumes, but need `prep` again to refill shared caches. Docker refuses removal of a volume still attached to a container. If listing or removal fails, clean reports the error and retains the prep confirmation record; a removal error can occur after some of the listed volumes were already deleted.
