# localllm

Run coding agents (opencode, pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Network containment, secret masking and git-hook protection are verified before every session, and changed files that run things are reported after it. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8080

## Usage

```sh
go install ./cmd/localllm
localllm build                         # build the sandbox images
localllm build [dir]                   # build dir's image from its localllm.Dockerfile
localllm prep [dir]                    # install dependencies (network, no agent)
localllm run [dir]                     # opencode in dir (default: current dir)
localllm run [dir] -- opencode run "…" # any command inside the sandbox
localllm run --ro ../specs [dir]       # also mount ../specs read-only at /ro/specs
localllm run [dir] -- opencode -c      # continue the project's last opencode session
localllm run --model qwen3.5-2b-instruct [dir]  # start with another model
localllm run --auto [dir]              # opencode acts without asking
localllm run --agent pi [dir]          # pi instead of opencode (always full auto)
localllm run --agent pi [dir] -- pi -c # continue the project's last pi session
localllm shell [dir]                   # interactive Bash in the same offline sandbox
localllm shell --ro ../specs [dir]     # shell with a read-only reference mount
localllm check [dir]                   # show mounts and run the containment probes
localllm clean [dir]                   # remove project state/dependency volumes after confirmation
localllm clean --shared-caches [dir]   # also reset shared caches; requires all sessions idle
```

`localllm shell [dir]` opens interactive Bash in `/workspace` as your UID, without starting an agent. It shares `run`'s project image, mounts, secret masks, dependency volumes, gateway access and containment probes. The probes must pass before the shell opens. Use `--agent pi` to select pi's config and state volume (opencode is the default); `--model`, `--gateway` and `--ro` work as for `run`. Agent configuration is rendered from the gateway's model list as usual, so the gateway must be available. Shell startup skips Bash profile and rc files. On exit, changed files that can run code on the host are reported, as for `run`. `--auto`, `--unsafe-skip-check` and command overrides are only available on `run`.

Set the gateway with `--gateway URL` or `LOCALLLM_GATEWAY` (default `http://host.docker.internal:8080`).

If llama-swap has `apiKeys` enabled, give localllm the key with `LOCALLLM_GATEWAY_KEY`, or with `LOCALLLM_GATEWAY_KEY_FILE` pointing to a file that contains it (keep the file outside your projects; surrounding whitespace is trimmed). Without either, nothing changes. The key is handed to `docker compose` in its environment only: the compose file names the variable without a value, so the key isn't written to disk or put on a command line. Inside the sandbox it is `$LOCALLLM_GATEWAY_KEY`, which the agent configs refer to by name (`{env:...}` for opencode, `$...` for pi), and the model list fetch and gateway probe send it as `Authorization: Bearer`. The agent can read it, so use a key meant only for the gateway. `check` shows whether a key is configured, never its value. If the gateway answers 401, `run` and `check` say so and point to these variables. `prep` never gets the key, so install scripts can't use your models through it.

The project must be inside the allowed root, `~/Projects` by default (set `LOCALLLM_ALLOWED_ROOT` to change it). Paths are resolved through symlinks first. `$HOME`, `/` and the allowed root itself are refused. `--ro PATH` (repeatable) can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories, or Docker socket locations. Each `--ro` path is mounted at `/ro/<name>`.

## How it works

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

By default opencode asks before it edits a file or runs a shell command. `--auto` approves both, so it works without interruption. In a non-interactive `opencode run`, a request that needs approval is rejected. Before a full-auto session in a git project with uncommitted changes (modified, staged or untracked files inside the project), `run` lists them and asks you to type `y`. Anything else aborts before a container starts, so you can commit or stash first. Non-git projects aren't asked. The check doesn't run git on the host, where a filter or fsmonitor that an agent configured (in a repository it created, or through `.gitattributes`) would run. localllm finds the repository by looking for `.git` in the project and the directories above it, then runs `git status` in a throwaway container of the base image, with no network, no capabilities and read-only mounts of only the project and its git directory at their host paths. For a linked worktree or submodule checkout, that includes the git dir its `.git` file points to, which must pass the same path rules as `--ro`.

pi has no permission system: it always edits files and runs commands without asking, so the sandbox is its only boundary. `run --agent pi` says so at startup, and the uncommitted-changes confirmation above always applies to it. The containment probes run before pi sessions as for opencode.

Models come from the gateway. At launch, `run` reads `/v1/models` through the sandbox's forwarder, so a model added to llama-swap shows up in the agent without editing anything. `assets/models.yaml` adds what llama-swap doesn't report:
- the context window, maximum output and tool-call support per model;
- `agent: false` to hide a model (`zeta2.1`, a completion model);
- the default model (`qwen3.6-coder`) and the small model opencode uses for titles (`qwen3.5-2b-instruct`). pi has no small model.

Models not listed there get conservative values from its `unknown` entry. `--model ID` picks another default for one session; an ID the gateway doesn't serve, or a hidden one, is refused with the list of available models. `localllm check` shows the resolved list. The generated config is written per session and mounted read-only.

The agent's config is versioned in this repo (`assets/agents/opencode/opencode.json`: permissions and defaults). Each session gets that config plus the gateway provider and model, mounted read-only at `/etc/localllm/opencode.json`. Your host `~/.config/opencode`, and any other host agent config, is never mounted. The agent's `HOME` is an empty tmpfs that is discarded when the session ends. opencode ignores the project's `opencode.json` and `.opencode/` directory inside the sandbox, because the agent could write them to change its own permissions. pi's config is versioned the same way (`assets/agents/pi/settings.json`). Each session renders `settings.json` (default model) and `models.json` (the gateway provider and its models), mounted read-only into pi's config directory `~/.pi/agent`. That directory is its own tmpfs, writable because pi creates lock files there, and discarded after the session. The base settings set `defaultProjectTrust` to `never`, so pi doesn't load the project's `.pi/` (settings, extensions, skills, system prompt), which the agent could write for a later session; a `.pi/` of your own isn't used in the sandbox either. `AGENTS.md` and other context files still load. pi's sessions are stored in its state volume via `PI_CODING_AGENT_SESSION_DIR`, which also outranks a `sessionDir` in the project's settings.

Agent state (sessions, logs, caches) lives in a Docker volume per agent per project, `localllm-state-<agent>-<hash of the project path>`, mounted at `/var/lib/localllm/state`. So a later `run` of the same project can resume its sessions, and other projects can't see them. The XDG cache dir is in the volume too, so tool caches such as Go's build cache persist per project. Volumes stay until you remove them with `localllm clean [dir]`. If the "state writable" probe fails after upgrading, run `localllm build` and remove the project's state volume, which an older image created owned by root.

Only one `run`, `shell`, `prep`, `check` or `clean` can use a project at a time. The registry validates the project and read-only mount paths once, locks that resolved project before creating mount placeholders or touching dependency volumes, and uses those same validated paths for preparation. It holds the lease through confirmations, startup, teardown and the change report. A second command is refused with the running operation, agent, host PID and start time. Symlink aliases share the same lock. Different projects can run concurrently.

Registry records live outside the sandbox in `localllm/registry/` under the host user cache directory. OS file locks release automatically when a host process dies; the next command for that project removes its orphaned Compose containers (including one-off and stopped containers) before starting. Recovery holds the per-project lock while calling Docker and releases the shared registry guard, allowing other projects to keep using the registry during a slow recovery. If Docker recovery fails, startup is refused and the stale record stays for the next attempt. State/dependency volumes and other projects' containers are preserved. Empty lock files remain after a clean exit; keep them in place, since deleting a lock file while it is in use can allow a second owner. Recovery does not remove old session directories or mount placeholders left by a killed process.

When a new agent session resolves a different model than another active agent session, startup warns that llama-swap will swap models back and forth. The comparison uses the model selected at startup, including `--model`; changes made later inside an agent aren't tracked. Errors recording or scanning models print a warning and the session continues. Registry entries that disappear during a scan are skipped. `shell`, `prep`, `check` and `clean` reserve the project but do not count as model users.

Before every `run` and `shell`, containment probes run in a fresh agent container. They try to reach the internet, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway, `/workspace` and the state volume work and that every `--ro` mount and the agent config reject writes. Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

The agent can't plant code that runs later on the host. `.git/hooks`, `.git/config` and `.envrc` are mounted read-only over themselves; a missing `.envrc` or hooks directory is replaced by an empty read-only placeholder, which is removed from the host after the session. A `core.hooksPath` inside the project (husky and similar) is read-only too, as are the hooks, config and `.git` pointer file of each submodule. `.git/commondir`, which would redirect git to another directory's config and hooks, is read-only too. When it is missing it is created with `.` (meaning "this git dir") for the session and removed afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself so it can't be renamed away and replaced. Linked worktrees' `commondir` (and `config.worktree` when enabled) are protected the same way. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. Git still works inside the sandbox: commit, branch and diff use your host identity, passed in through `GIT_CONFIG_*`. The probes check each of these mounts. Limits:

- The agent can create a git repository of its own: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. Set `git config --global safe.bareRepository explicit` to close the bare-repository case.
- In a linked worktree or a submodule checkout opened on its own, `.git` is a file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call, like husky's `.husky/pre-commit`, stay writable. So does an `.envrc` in a subdirectory. `run` warns when these change (see below).

Secrets are masked: inside the sandbox they read as an empty file or directory and can't be written, while the host files stay untouched. The default rules cover `.env` and `.env.*` (except `.env.example` and similar), keys and certificates (`*.pem`, `*.key`, `id_rsa*`, ...), `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, Terraform state, and `.aws/`, `.ssh/` and `.gnupg/` directories. Add your own in `.localllmignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` can unmask a default, and it is read-only inside the sandbox. `localllm check` lists the masks. Limits:

- Masks are computed at launch. A secret created on the host during a session isn't masked until the next one.
- Dependency and build directories (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...) aren't searched, to keep launch fast.
- A masked file tracked in git reads as empty inside the sandbox, so a commit made there would record it emptied. `run` and `check` warn about these.
- `--ro` paths aren't masked.

Other files that run things can't be locked without breaking normal work, so `run` reports them instead. It records them before the session and, when it ends, lists any that were added, modified or deleted. The list covers `package.json`, `Makefile`, `*.nix`, `flake.lock` and `.envrc` at any depth, `.pre-commit-config.yaml`, and the `.github/workflows/`, `.vscode/`, `.devcontainer/`, `.husky/` and `.githooks/` directories (`changereport.Watched`). Content is compared by hash, so restoring a timestamp doesn't hide an edit. Permission changes and symlink targets count too. Review the listed files before you build, install or open the project in an editor. Limits:

- It's a warning, not a block: the changes are already on the host.
- Dependency and build directories are skipped, as for masks. So is `.git`, which is protected by mounts.
- `check` doesn't report changes, and nothing is reported if `localllm` itself is killed mid-session.

## Dependencies

`run` is offline, so dependencies are installed beforehand with `localllm prep [dir]`. It runs in the same image, with the same masks and read-only `.git/hooks`, `.git/config`, `.envrc` and `localllm.yaml` as `run`, but on a normal network so registries are reachable, and without an agent, the forwarder or the gateway key. Run it again after changing a manifest. When the agent needs a new dependency, add it to the manifest on the host and re-run `prep`.

What it runs is detected from the project root:
- `package.json`: pnpm, yarn or npm, chosen by the `packageManager` field, else by lockfile (`pnpm-lock.yaml`, `yarn.lock`, otherwise npm). pnpm and yarn come from corepack. `packageManager` must be a plain version (`pnpm@10.0.0`, optionally `+sha512.…`), not a URL. With a lockfile, the install is frozen (`pnpm install --offline --frozen-lockfile`, `yarn install --frozen-lockfile` or `--immutable`, `npm ci`).
- `go.mod`: `go mod download`, in every module directory, or once at the root with a `go.work`. Keep `go.sum` complete (`go mod tidy` on the host).

To run something else, put the command in `localllm.yaml` at the project root:

```yaml
prep:
  command: pnpm install && pnpm build   # replaces the detected installs; run by sh in /workspace
  dirs: [.venv]                         # extra dependency directories (see below)
```

The command runs with network access, so `localllm.yaml` is read-only inside the sandbox, like `.localllmignore`. The detected fetches (below) still run before it.

Dependency directories (`node_modules` at the root and in each workspace package, plus `prep.dirs`) are Docker volumes per project, `localllm-deps-<hash of the project path>-<hash of the directory>`, mounted over those paths in `prep`, `run` and `check`. Linux binaries never land in your project directory. A `node_modules` you have on the host stays untouched and isn't visible in the sandbox. A dependency directory may not be or contain a protected or masked path, or lie inside one, and may not go through a symlink. `prep` deletes and recreates these volumes each time, so nothing an agent session wrote into them runs during the install. It fails while a session of the project is running.

Caches are shared by all projects: the Go module cache (`localllm-cache-gomod`, `GOMODCACHE`), corepack's pnpm and yarn releases (`localllm-cache-corepack`), and the pnpm store (`localllm-cache-pnpm-store`). So that no project can plant code in another project's dependencies, only package manager code ever writes them. `prep` runs in two phases:
1. Fetch, with the caches writable: `corepack install`, `pnpm fetch --ignore-pnpmfile` and `go mod download`. No project code runs: no lifecycle scripts, pnpmfile or `yarnPath`. What is downloaded is checked against the lockfile, `go.sum` or the `packageManager` hash, and the caches are keyed by content or version.
2. Install, with the caches read-only: `pnpm install --offline`, `yarn install`, `npm ci` and the `localllm.yaml` command, which run lifecycle scripts.

pnpm without a lockfile has nothing to fetch ahead, so its install uses a store of its own for that run; the next `prep` then uses the shared one. `run` mounts the Go module and corepack caches read-only, and sets `GOPROXY=off` and `COREPACK_ENABLE_NETWORK=0` so a missing dependency fails at once. `run` and `check` print a hint when a dependency directory is empty though its manifest exists.

The agent can write the files that decide what `prep` runs, and `prep` runs them with network access. So `prep` asks you to type `y` first when:
- it is the project's first `prep` (it lists the commands and the project code the install runs);
- a file that decides what prep runs changed since the last successful `prep`: `package.json` files, lockfiles, `pnpm-workspace.yaml`, `.pnpmfile.cjs`, `.npmrc`, `.yarnrc.yml`, `.yarn/releases/`, `.yarn/plugins/`, patches, `go.mod`, `go.sum`, `go.work` or `localllm.yaml`;
- the install runs project code (lifecycle scripts like `postinstall` or `prepare`, a pnpmfile, a `yarnPath`, the `localllm.yaml` command) and any other project file changed, since that code may run it.

It lists what changed; review it (`git diff`) before you answer. Anything else, or no terminal, aborts. The state after each `prep` is recorded in `~/Library/Caches/localllm/prep/` (the user cache dir), outside the sandbox.

Limits:
- `prep` has the same network as any container: the internet, the LAN and the host's loopback services (including the gateway port, but without the key). It runs no containment probes.
- Changes in dependency and build directories (`dist`, `build`, `vendor`, ...) don't count for the confirmation, as for the change report. Don't let install scripts run files from there.
- The confirmation is only as good as your review. A dependency from the registry can still run its own install scripts during its project's `prep`, with the masks in place.
- Install scripts can't change `.git/config`, so a `prepare` script that sets `core.hooksPath` (husky) fails or does nothing; run it on the host.
- `.npmrc` is masked by default, so registry settings and tokens in it don't apply. Unmask it in `.localllmignore` (`!.npmrc`) if `prep` needs it; the agent can then read it too.
- `prep` reports changed files that run things, like `run`.

## Cleanup

`localllm clean [dir]` lists the project's existing agent state and dependency volumes, then asks you to type `y`. Declining or reaching end of input leaves them in place. It removes state for both agents and dependency volumes for directories no longer mentioned in the current configuration. The selected volume names must match the exact hash of the resolved project path. It also resets the host's prep confirmation record, so the next `prep` asks for approval again. When there are no volumes or prep record, it reports that there is nothing to clean.

Cleanup holds the project's session lease through listing, confirmation and removal. It refuses while a run, shell, prep or check is using that project. It works without a model gateway and without parsing the project's dependency configuration, so it can clean up after manifests or configuration have been removed or broken. After cleanup, `run` creates fresh state and empty dependency volumes; run `prep` again to restore dependencies. Project images and source files are preserved.

Shared caches are retained by default. `localllm clean --shared-caches [dir]` includes the Go module cache, corepack releases and pnpm store in the confirmed list. It requires exclusive access: any starting, running or finishing session prevents the reset, and a reset in progress prevents new sessions from starting. The cache reservation covers confirmations and gaps between prep containers too. Other projects keep their own state and dependency volumes, but need `prep` again to refill shared caches. Docker refuses removal of a volume still attached to a container. If listing or removal fails, clean reports the error and retains the prep confirmation record; a removal error can occur after some of the listed volumes were already deleted.

## Project image

A project that needs more than Node and Go can add a `localllm.Dockerfile` at its root, on top of the base image:

```dockerfile
FROM localllm-base:latest
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*
```

`localllm build dir` builds it as `localllm-project-<hash of the project path>`, and `prep`, `run`, `shell` and `check` of that project then use it (`check` shows the image). Projects without the file use the base image. The build runs as root with network access, like any `docker build`, but with an empty build context, so `COPY` and `ADD` can't read the project or its masked secrets. Download what you need in a `RUN`, or copy it from another image (`COPY --from=...`). The file is read-only inside the sandbox, like `localllm.yaml`, so the agent can't change what the next build runs. A symlinked `localllm.Dockerfile` is refused.

The image is labeled with the Dockerfile's hash and the base image it was built on. When the Dockerfile has changed or `localllm build` has rebuilt the base image since, sessions warn and use the old image. Without an image yet, they warn and use the base image. Each warning shows the `localllm build dir` command that fixes it. Run it again to pick up a new base image even when the Dockerfile is unchanged. The sandbox sets the user, capabilities and networks, so `USER` or `EXPOSE` in the Dockerfile don't change them. The root container that prepares dependency volumes always uses the base image.

### Rust

Copy the toolchain from the official image, the way the base image gets Go, and keep Cargo's downloads and build output in dependency volumes:

```dockerfile
FROM rust:1-slim-trixie AS rust

FROM localllm-base:latest
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo
# The toolchain stays in the image; the registry and git checkouts go in
# the .cargo-home dependency volume.
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/workspace/.cargo-home \
    PATH=/usr/local/cargo/bin:$PATH
```

```yaml
# localllm.yaml
prep:
  command: cargo fetch
  dirs: [.cargo-home, target]
```

Pin the `rust` tag to a version for reproducible builds. `cargo fetch` needs a `Cargo.lock`. In `run`, `cargo build` and `cargo test` then work offline; add `--offline` if Cargo tries the network anyway.

To make Rust available to every project instead, add the same `COPY` and `ENV` lines to `assets/images/base/Dockerfile`, and, for a registry cache shared across projects like the Go module cache, a `CARGO_HOME` entry in `sandbox.sharedCaches` with `cargo fetch` in the fetch phase.

## Tests

Integration tests need Docker: `go test -tags integration ./...`. The prep and project image tests also need network access.
