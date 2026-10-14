# localllm

Run coding agents (opencode, later pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Network containment, secret masking and git-hook protection are verified before every session. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

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

Before every `run`, containment probes run in a fresh agent container. They try to reach the internet, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway and `/workspace` work and that every `--ro` mount rejects writes. Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

The agent can't plant code that runs later on the host. `.git/hooks`, `.git/config` and `.envrc` are mounted read-only over themselves; a missing `.envrc` or hooks directory is replaced by an empty read-only placeholder, which is removed from the host after the session. A `core.hooksPath` inside the project (husky and similar) is read-only too, as are the hooks, config and `.git` pointer file of each submodule. `.git/commondir`, which would redirect git to another directory's config and hooks, is read-only too. When it is missing it is created with `.` (meaning "this git dir") for the session and removed afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself so it can't be renamed away and replaced. Linked worktrees' `commondir` (and `config.worktree` when enabled) are protected the same way. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. Git still works inside the sandbox: commit, branch and diff use your host identity, passed in through `GIT_CONFIG_*`. The probes check each of these mounts. Limits:

- The agent can create a git repository of its own: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. Set `git config --global safe.bareRepository explicit` to close the bare-repository case.
- In a linked worktree or a submodule checkout opened on its own, `.git` is a file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call, like husky's `.husky/pre-commit`, stay writable. So does an `.envrc` in a subdirectory.

Secrets are masked: inside the sandbox they read as an empty file or directory and can't be written, while the host files stay untouched. The default rules cover `.env` and `.env.*` (except `.env.example` and similar), keys and certificates (`*.pem`, `*.key`, `id_rsa*`, ...), `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, Terraform state, and `.aws/`, `.ssh/` and `.gnupg/` directories. Add your own in `.localllmignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` can unmask a default, and it is read-only inside the sandbox. `localllm check` lists the masks. Limits:

- Masks are computed at launch. A secret created on the host during a session isn't masked until the next one.
- Dependency and build directories (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...) aren't searched, to keep launch fast.
- A masked file tracked in git reads as empty inside the sandbox, so a commit made there would record it emptied. `run` and `check` warn about these.
- `--ro` paths aren't masked.

Integration tests need Docker: `go test -tags integration ./...`.
