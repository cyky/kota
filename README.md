# localllm

Run coding agents (opencode, later pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Network containment, secret masking and git-hook protection are verified before every session, and changed files that run things are reported after it. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

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
localllm run [dir] -- opencode -c      # continue the project's last opencode session
localllm run --model qwen3.5-2b-instruct [dir]  # start with another model
localllm run --auto [dir]              # opencode acts without asking
localllm check [dir]                   # show mounts and run the containment probes
```

Set the gateway with `--gateway URL` or `LOCALLLM_GATEWAY` (default `http://host.docker.internal:8080`).

The project must be inside the allowed root, `~/Projects` by default (set `LOCALLLM_ALLOWED_ROOT` to change it). Paths are resolved through symlinks first. `$HOME`, `/` and the allowed root itself are refused. `--ro PATH` (repeatable) can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories, or Docker socket locations. Each `--ro` path is mounted at `/ro/<name>`.

## How it works

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

By default opencode asks before it edits a file or runs a shell command. `--auto` approves both, so it works without interruption. In a non-interactive `opencode run`, a request that needs approval is rejected. Before a full-auto session in a git project with uncommitted changes (modified, staged or untracked files inside the project), `run` lists them and asks you to type `y`. Anything else aborts before a container starts, so you can commit or stash first. Non-git projects aren't asked.

Models come from the gateway. At launch, `run` reads `/v1/models` through the sandbox's forwarder, so a model added to llama-swap shows up in opencode without editing anything. `assets/models.yaml` adds what llama-swap doesn't report:
- the context window, maximum output and tool-call support per model;
- `agent: false` to hide a model (`zeta2.1`, a completion model);
- the default model (`qwen3.6-coder`) and the small model opencode uses for titles (`qwen3.5-2b-instruct`).

Models not listed there get conservative values from its `unknown` entry. `--model ID` picks another default for one session; an ID the gateway doesn't serve, or a hidden one, is refused with the list of available models. `localllm check` shows the resolved list. The generated config is written per session and mounted read-only.

The agent's config is versioned in this repo (`assets/agents/opencode/opencode.json`: permissions and defaults). Each session gets that config plus the gateway provider and model, mounted read-only at `/etc/localllm/opencode.json`. Your host `~/.config/opencode`, and any other host agent config, is never mounted. The agent's `HOME` is an empty tmpfs that is discarded when the session ends. opencode ignores the project's `opencode.json` and `.opencode/` directory inside the sandbox, because the agent could write them to change its own permissions. Agent state (sessions, logs, caches) lives in a Docker volume per agent per project, `localllm-state-opencode-<hash of the project path>`, mounted at `/var/lib/localllm/state`. So a later `run` of the same project can resume its sessions, and other projects can't see them. The XDG cache dir is in the volume too, so tool caches such as Go's build cache persist per project. Volumes stay until you remove them (`docker volume ls`, `docker volume rm`). If the "state writable" probe fails after upgrading, run `localllm build` and remove the project's state volume, which an older image created owned by root.

Before every `run`, containment probes run in a fresh agent container. They try to reach the internet, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway, `/workspace` and the state volume work and that every `--ro` mount and the agent config reject writes. Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

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

Integration tests need Docker: `go test -tags integration ./...`.
