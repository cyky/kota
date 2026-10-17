# kota

Run coding agents (opencode, pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

A kota is a Finnish (and Sámi) hut: a simple shelter where the agent works, apart from everything else.

Status: early. Network containment, secret masking and git-hook protection are verified before every session, and changed files that run things are reported after it. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8686
- QEMU, only for projects with an Engine VM (see below)

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
kota run --no-history [dir]        # on a copy of the last commit, without git history (also for shell)
kota check [dir]                   # show mounts, the Engine VM and run the containment probes
kota clean [dir]                   # remove project state/dependency volumes and engine data after confirmation
kota clean --shared-caches [dir]   # also reset shared caches; requires all sessions idle
kota instructions                  # print the agent's working style and whether it's your override
```

`kota shell [dir]` opens interactive Bash in `/workspace` as your UID, without starting an agent. It shares `run`'s project image, mounts, secret masks, dependency volumes, gateway access and containment probes. The probes must pass before the shell opens. Use `--agent pi` to select pi's config and state volume (opencode is the default); `--model`, `--gateway`, `--ro`, `--no-engine` and `--no-history` work as for `run`. Agent configuration is rendered from the gateway's model list as usual, so the gateway must be available. Shell startup skips Bash profile and rc files. On exit, changed files that can run code on the host are reported, as for `run`. `--auto`, `--unsafe-skip-check` and command overrides are only available on `run`.

Set the gateway with `--gateway URL` or `KOTA_GATEWAY` (default `http://host.docker.internal:8686`).

If llama-swap has `apiKeys` enabled, give kota the key with `KOTA_GATEWAY_KEY`, or with `KOTA_GATEWAY_KEY_FILE` pointing to a file that contains it (keep the file outside your projects; surrounding whitespace is trimmed). Without either, nothing changes. The key is handed to `docker compose` in its environment only: the compose file names the variable without a value, so the key isn't written to disk or put on a command line. Inside the sandbox it is `$KOTA_GATEWAY_KEY`, which the agent configs refer to by name (`{env:...}` for opencode, `$...` for pi), and the model list fetch and gateway probe send it as `Authorization: Bearer`. The agent can read it, so use a key meant only for the gateway. `check` shows whether a key is configured, never its value. If the gateway answers 401, `run` and `check` say so and point to these variables. `prep` never gets the key, so install scripts can't use your models through it.

The project must be inside the allowed root, `~/Projects` by default (set `KOTA_ALLOWED_ROOT` to change it). Paths are resolved through symlinks first. `$HOME`, `/` and the allowed root itself are refused. `--ro PATH` (repeatable) can point anywhere except at or around `$HOME`, the project, `~/.ssh`, `~/.config`, other credential directories, or Docker socket locations. Each `--ro` path is mounted at `/ro/<name>`.

## Migrating from localllm

kota was called localllm. It starts fresh: it doesn't read localllm's volumes, images, data dir, project files or environment variables. To switch:

1. Before installing kota, run `localllm clean [dir]` in each project you used it in, and `localllm clean --shared-caches [dir]` once to remove the shared caches.
2. Rename the project files: `localllm.yaml` to `kota.yaml`, `.localllmignore` to `.kotaignore` and `localllm.Dockerfile` to `kota.Dockerfile`. kota ignores the old names, so a project that still has only them runs without its configuration, masks or image.
3. Rename the environment variables: `LOCALLLM_GATEWAY`, `LOCALLLM_GATEWAY_KEY`, `LOCALLLM_GATEWAY_KEY_FILE` and `LOCALLLM_ALLOWED_ROOT` become `KOTA_*`.
4. Remove what's left: the old images (`docker rmi localllm-base localllm-forwarder` and the project images, `docker images 'localllm-project-*'`), the old data dir (`localllm/` in the user cache dir, `~/Library/Caches/localllm` on macOS, including the Engine base disk) and the `localllm` binary (`rm "$(go env GOPATH)/bin/localllm"`).
5. Install kota and run `kota build`, then `kota prep` in each project.

## How it works

Diagrams of the runtime, the command flows, the packages and where data lives are in [docs/architecture.md](docs/architecture.md). An overview of how the sandbox protects the host, and its limits, is in [docs/sandbox.md](docs/sandbox.md).

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

By default opencode asks before it edits a file or runs a shell command. `--auto` approves both, so it works without interruption. In a non-interactive `opencode run`, a request that needs approval is rejected. Before a full-auto session in a git project with uncommitted changes (modified, staged or untracked files inside the project), `run` lists them and asks you to type `y`. Anything else aborts before a container starts, so you can commit or stash first. Non-git projects aren't asked. The check doesn't run git on the host, where a filter or fsmonitor that an agent configured (in a repository it created, or through `.gitattributes`) would run. kota finds the repository by looking for `.git` in the project and the directories above it, then runs `git status` in a throwaway container of the base image, with no network, no capabilities and read-only mounts of only the project and its git directory at their host paths. For a linked worktree or submodule checkout, that includes the git dir its `.git` file points to, which must pass the same path rules as `--ro`.

pi has no permission system: it always edits files and runs commands without asking, so the sandbox is its only boundary. `run --agent pi` says so at startup, and the uncommitted-changes confirmation above always applies to it. The containment probes run before pi sessions as for opencode.

Models come from the gateway. At launch, `run` reads `/v1/models` through the sandbox's forwarder, so a model added to llama-swap shows up in the agent without editing anything. `assets/models.yaml` adds what llama-swap doesn't report:
- the context window, maximum output and tool-call support per model;
- `agent: false` to hide a model (`zeta2.1`, a completion model);
- the default model (`qwen3.6-coder`) and the small model opencode uses for titles (`qwen3.5-2b-instruct`). pi has no small model.

Models not listed there get conservative values from its `unknown` entry. `--model ID` picks another default for one session; an ID the gateway doesn't serve, or a hidden one, is refused with the list of available models. `kota check` shows the resolved list. The generated config is written per session and mounted read-only.

The agent's config is versioned in this repo (`assets/agents/opencode/opencode.json`: permissions and defaults). Each session gets that config plus the gateway provider and model, mounted read-only at `/etc/kota/opencode.json`. Your host `~/.config/opencode`, and any other host agent config, is never mounted. The agent's `HOME` is an empty tmpfs that is discarded when the session ends. opencode ignores the project's `opencode.json` and `.opencode/` directory inside the sandbox, because the agent could write them to change its own permissions. pi's config is versioned the same way (`assets/agents/pi/settings.json`). Each session renders `settings.json` (default model) and `models.json` (the gateway provider and its models), mounted read-only into pi's config directory `~/.pi/agent`. That directory is its own tmpfs, writable because pi creates lock files there, and discarded after the session. The base settings set `defaultProjectTrust` to `never`, so pi doesn't load the project's `.pi/` (settings, extensions, skills, system prompt), which the agent could write for a later session; a `.pi/` of your own isn't used in the sandbox either. `AGENTS.md` and other context files still load. pi's sessions are stored in its state volume via `PI_CODING_AGENT_SESSION_DIR`, which also outranks a `sessionDir` in the project's settings.

### What the agent is told

Every `run` and `shell` session gives the agent kota's instructions, the same for both agents. opencode gets them as a global `~/.config/opencode/AGENTS.md`, and pi as `APPEND_SYSTEM.md` in its agent directory. Both are mounted read-only, and `check` verifies that. The project's own `AGENTS.md` still loads as well, so put project specifics there. The file has three parts, in this order:

1. **Working on tasks** (built in, `assets/agents/instructions.md`). Work from the project's tickets, one at a time, and keep progress and decisions in the ticket rather than the agent's context, which is summarized when it fills up. Work test-first, commit after each green step, and report checks that couldn't run as not run.
2. **Working unattended** (built in). Applies only when you tell the agent so in the conversation ("work unattended", "AFK", "I'm away", "don't wait for me"). The agent doesn't ask questions: when blocked, it writes the blocker in the ticket, sets it to needs-info and moves on. It stops when no tickets are left and summarizes.
3. **This sandbox** (always added by kota). The session is offline. Dependencies are local, so it reads their source instead of guessing APIs. Nothing can be installed, so for a missing package it names the package, version and reason, and asks you to add it on the host and run `kota prep`. It doesn't edit the manifest or lockfile for it, since a lockfile it can't update breaks the next `prep`. In an engine project, the Engine VM note follows (see [Engine VM](#engine-vm)).

To use your own working style, write it to `$XDG_CONFIG_HOME/kota/instructions.md` (`~/.config/kota/instructions.md` when `XDG_CONFIG_HOME` is unset or not an absolute path). It replaces parts 1 and 2 for every project. Part 3 is still added, so an override can't drop the Sandbox facts. The file is read on the host and never mounted, so the agent can't change it. If it isn't a regular file, or is larger than 64 KiB, `run`, `shell` and `check` stop with its path.

`kota instructions` prints parts 1 and 2 as they are in effect to stdout, and says on stderr whether they are the `built-in default` or `override: <path>`. `kota instructions > ~/.config/kota/instructions.md` gives you a starting point. `check` shows the source too.

Agent state (sessions, logs, caches) lives in a Docker volume per agent per project, `kota-state-<agent>-<hash of the project path>`, mounted at `/var/lib/kota/state`. `--no-history` sessions have their own, `kota-state-<agent>-nohistory-<hash>`. So a later `run` of the same project can resume its sessions, and other projects can't see them. The XDG cache dir is in the volume too, so tool caches such as Go's build cache persist per project. Volumes stay until you remove them with `kota clean [dir]`. If the "state writable" probe fails after upgrading, run `kota build` and remove the project's state volume, which an older image created owned by root.

Only one `run`, `shell`, `prep`, `check` or `clean` can use a project at a time. The registry validates the project and read-only mount paths once, locks that resolved project before creating mount placeholders or touching dependency volumes, and uses those same validated paths for preparation. It holds the lease through confirmations, startup, teardown and the change report. A second command is refused with the running operation, agent, host PID and start time. Symlink aliases share the same lock. Different projects can run concurrently.

Registry records live outside the sandbox in `kota/registry/` under the host user cache directory. OS file locks release automatically when a host process dies; the next command for that project removes its orphaned Compose containers (including one-off and stopped containers) before starting. Recovery holds the per-project lock while calling Docker and releases the shared registry guard, allowing other projects to keep using the registry during a slow recovery. If Docker recovery fails, startup is refused and the stale record stays for the next attempt. State/dependency volumes and other projects' containers are preserved. Empty lock files remain after a clean exit; keep them in place, since deleting a lock file while it is in use can allow a second owner.

Ctrl-C, `kill` (SIGTERM) or a closed terminal (SIGHUP) doesn't leave anything behind. Every confirmation is asked before anything is written to the host or a sandbox container starts, and Ctrl-C at a prompt counts as no. Once a session is starting, a signal stops it at the next step: Docker commands in progress are stopped, the agent is never started after an interrupted startup, and the containers, networks, placeholders and session directory are removed as on an error. The change report still runs, and the lease is released. kota then exits with status 130. Inside the agent's terminal session, Ctrl-C goes to the agent as usual. Only a process killed outright (SIGKILL, a crash) skips this. Recovery then removes its containers, but not its session directory or the placeholders and seeded files it left in the project (`git status --ignored` shows them).

When a new agent session resolves a different model than another active agent session, startup warns that llama-swap will swap models back and forth. The comparison uses the model selected at startup, including `--model`; changes made later inside an agent aren't tracked. Errors recording or scanning models print a warning and the session continues. Registry entries that disappear during a scan are skipped. `shell`, `prep`, `check` and `clean` reserve the project but do not count as model users.

Before every `run` and `shell`, containment probes run in a fresh agent container. They try to reach the internet over IPv4 and IPv6, external DNS (A and AAAA lookups), the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway, `/workspace` and the state volume work and that every `--ro` mount and the agent config reject writes. For a project with an Engine VM, its own probes follow (see [Engine VM](#engine-vm)). Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

The agent can't plant code that runs later on the host. `.git/hooks`, `.git/config` and `.envrc` are mounted read-only over themselves; a missing `.envrc` or hooks directory is replaced by an empty read-only placeholder, which is removed from the host after the session. A `core.hooksPath` inside the project (husky and similar) is read-only too, as are the hooks, config and `.git` pointer file of each submodule. `.git/commondir`, which would redirect git to another directory's config and hooks, is read-only too. When it is missing it is created with `.` (meaning "this git dir") for the session and removed afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself so it can't be renamed away and replaced. Linked worktrees' `commondir` (and `config.worktree` when enabled) are protected the same way. So is every file inside the project that git config includes (`include.path`, `includeIf.*.path`), from any config level (system, global, the repository's, a submodule's) and through any chain of includes. Conditional includes count even when their condition doesn't hold now, since the agent could make it hold. A missing include target gets a placeholder. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. Git still works inside the sandbox: commit, branch and diff use your host identity, passed in through `GIT_CONFIG_*`. Git's excludes file there is a read-only list of the session's placeholders and masks, so `git add -A` doesn't stage them. Your host's global excludes (`core.excludesFile`, `~/.config/git/ignore`) don't apply inside the sandbox; they aren't passed in, since even their patterns can name private files. kota warns instead when a commit adds a file they ignore (see below). The probes check each of these mounts. Limits:

- The agent can create a git repository of its own: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. Set `git config --global safe.bareRepository explicit` to close the bare-repository case.
- In a linked worktree or a submodule checkout opened on its own, `.git` is a file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call, like husky's `.husky/pre-commit`, stay writable. So does an `.envrc` in a subdirectory. `run` warns when these change (see below).
- Git config values that run a command can name a project script, which stays writable: `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `core.askPass`, `sequence.editor`, `diff.external`, `gpg.program`, `credential.helper`, `alias.* = !...`, `filter.*.clean|smudge|process`, `diff.*.textconv|command` and `merge.*.driver`. When one names a path inside the project (as its command, or as an argument that exists), `run`, `shell`, `check` and `prep` warn with the key, the config file and the script, and a change to that script is reported after the session. Paths are found by splitting the command into words, so a script reached through a variable or another program's config isn't seen.

Secrets are masked: inside the sandbox they read as an empty file or directory and can't be written, while the host files stay untouched. The default rules cover `.env` and `.env.*` (except `.env.example` and similar), keys and certificates (`*.pem`, `*.key`, `id_rsa*`, ...), `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, Terraform state, and `.aws/`, `.ssh/` and `.gnupg/` directories. Add your own in `.kotaignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` can unmask a default, and it is read-only inside the sandbox. `kota check` lists the masks. Limits:

- Masks are computed at launch. A secret created on the host during a session isn't masked until the next one.
- Dependency and build directories (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...) aren't searched, to keep launch fast.
- Masks don't reach git. A secret that is tracked, or was ever committed, can be read from `.git` inside the sandbox (`git log -p -- .env`), and a commit made there would record a tracked one emptied. `run`, `shell`, `check` and `prep` warn about masked files that are tracked now, but history isn't checked. A commit that empties one is reported after the session (see below). To fix it, remove the file from the index (`git rm --cached`) and from history (for example with `git filter-repo`), and rotate the secret, since earlier clones and pushes still have it. To keep the history out of a session, use `--no-history` (see [Sessions without git history](#sessions-without-git-history)).
- `--ro` paths aren't masked.

Other files that run things can't be locked without breaking normal work, so `run` reports them instead. It records them before the session and, when it ends, lists any that were added, modified or deleted. The list covers, at any depth, `package.json`, `go.mod`, `Makefile`, `*.nix`, `flake.lock`, `.envrc`, mise's `mise.toml`, `.mise.toml`, `mise.local.toml` and `.tool-versions`, `.gitattributes`, `.gitmodules`, `build.rs`, `justfile` and `Justfile`, `Taskfile.yml`, `.pre-commit-config.yaml`, and the `.cargo/` and `.idea/` directories. It also covers the `.github/workflows/`, `.vscode/`, `.devcontainer/`, `.husky/` and `.githooks/` directories (`changereport.Watched`). Content is compared by hash, so restoring a timestamp doesn't hide an edit. Permission changes and symlink targets count too. Under each file the report says what changed, or how to see it:

- A modified `package.json` lists each dependency (`dependencies`, `devDependencies`, `optionalDependencies`, `peerDependencies`) and script that was added, removed or changed, with its old and new value. Scripts that the install runs, such as `postinstall` and `prepare`, are marked `(runs on install)`.
- A modified `go.mod` lists changed `go`, `toolchain`, `require`, `replace` and `tool` lines.
- Any other file, including lockfiles, gets the command that shows the change: `git -C <project> diff HEAD -- <path>`, or `git status` for an added file. kota doesn't print the diff itself, because the file was written by the agent. A manifest gets the command too when it can't be parsed before or after the session, or when none of the lines above changed.
- Names and values are escaped and long ones are cut, so an agent-written file can't send control sequences to your terminal. Manifest contents are read on the host before the session starts.

```
WARNING: the session changed 2 file(s) that can run code on the host. ...
  modified  .envrc
      see: git -C /home/me/app diff HEAD -- .envrc
  modified  package.json
      devDependencies: added wrangler ^4.0.0
      scripts: added postinstall (runs on install) "node x.js"
```

Review the listed files before you build, install or open the project in an editor. Limits:

- It's a warning, not a block: the changes are already on the host.
- Dependency and build directories are skipped, as for masks. So is `.git`, which is protected by mounts.
- `check` doesn't report changes, and nothing is reported if `kota` itself is killed outright (SIGKILL) mid-session.

Commits are reported too, after `run`, `shell` and `prep` (a dependency install script can commit as well as an agent can). In a project with its own `.git` directory, kota records every local branch and tag and `HEAD` before the command, and after it lists the branches and tags that were created, moved or deleted, and every commit reachable now that wasn't before, on any branch or a detached `HEAD` (at most 20 are listed). When `HEAD` no longer contains its previous commit (a reset, rebase, amend or branch switch), the report says so and names the commit, so you can get it back with `git reset --hard`. It then warns about paths in those commits that need review:

- a placeholder kota mounted, or a masked secret: added, deleted, modified or emptied. The files still have their real content on disk, so the report prints the `git rm --cached` or `git add` that fixes them in a new commit.
- a file your host's global git excludes ignore, added. Those excludes are read from your global git config only, never the project's, and matched on the host.
- a watched file (the list above) changed in a commit when the working tree doesn't show it, for example a workflow edited, committed and then put back. The change runs once the commit is checked out, merged or pushed. Watched files the working tree report already lists aren't repeated.

None of this changes kota's exit status, and kota never amends, reverts or blocks a commit. Git doesn't run on the host against the project for this: the refs and commits are read in a throwaway container of the base image, offline and read-only, with textconv, external diff drivers and fsmonitor off. Remote-tracking refs and stashes aren't checked, and merge commits list no files of their own.

## Sessions without git history

Inside a normal session the agent can read the project's whole git history, including a secret that was committed once and is masked or deleted now. `kota run --no-history [dir]` (or `shell --no-history`) runs the session on a copy of the project's last commit instead: a working git repository with that one commit, and no older commits, remotes, git config, hooks or reflog of the original. The project must be the top of a git repository with at least one commit.

The copy is made by `git clone --depth 1` in a throwaway container of the base image, offline, with the project mounted read-only, so git never runs on the host against the project. It lives in `copies/` in kota's data dir (`~/Library/Caches/kota/copies/<project>-<YYYY-MM-DDTHHMMSS>` on macOS) and must pass the same path rules as a project, with that directory as its allowed root. The session runs on the copy with the usual masks, protections, probes and Engine VM.

The copy holds only what is committed. Before it is made, kota lists what won't be in it, and asks you to type `y`:
- uncommitted changes (modified, staged, or untracked and not ignored), as the full-auto check lists them. With `--auto` this replaces the full-auto question: the agent works on the copy, so it can't overwrite your changes.
- submodules, which are empty directories in the copy;
- files stored in Git LFS, which are pointer files in the copy.

Ignored files (`.env`, local overrides) aren't listed, and aren't in the copy either.

Everything else belongs to the original project: `.kotaignore`, `kota.yaml` and `kota.Dockerfile` are taken from it, even when they aren't committed, so an untracked `.kotaignore` still masks a tracked secret in the copy. They are read-only in the copy, as in a normal session. `prep` runs on the original as usual, and the session uses its dependency volumes, prep record, project image and Engine VM, and its git identity. Only the agent's state is separate (see above), so transcripts from normal sessions, which may hold `git log -p` output, don't reach the copy. The project's lease is held for the whole session.

What the copy doesn't hide: the last commit itself, so a secret that is tracked now is still readable through git (kota warns about tracked masked files as usual), and anything the agent learned in an earlier normal session and wrote into the project.

After the session, the commits on the copy's `HEAD` are brought back into the project as a branch, `kota/<agent>-<YYYY-MM-DDTHHMMSS>` (`kota/shell-...` after `shell`), named after the session's start. It is never checked out or merged: your working tree and current branch stay as they were, and you review and merge the branch yourself. Only `HEAD` comes back. Other branches and tags made in the copy are listed as not brought back.

The agent controlled the copy, its git config and hooks included, so the return never runs git on the host, and never runs git in the copy with your repository writable. Two throwaway containers of the base image do it, offline. The first runs `git bundle create` in the copy, mounted read-only, and can write only an empty directory for the bundle. The second fetches the bundle into your repository, with only its git directory writable, without the copy, and with hooks, fsmonitor and automatic gc off. It checks the objects (`transfer.fsckObjects`), and makes the branch only if it descends from the commit the copy was made from, and doesn't exist yet. No branch is made when there are no new commits, or when the agent reset or replaced the history.

The change report lists the copy's branches, tags and commits with the usual warnings. Since the copy's working tree is thrown away, a watched file changed in a commit is always flagged. Then it names the branch, or why there is none. The copy is deleted, unless it has uncommitted changes, branches or tags that weren't brought back, or commits that couldn't be. Then it is kept, and the report gives its path, the reason and the uncommitted changes. git in a kept copy reads config and hooks the agent could write, so look at them before you run git there. `kota clean [dir]` removes the project's kept copies, after listing them.

## Dependencies

`run` is offline, so dependencies are installed beforehand with `kota prep [dir]`. It runs in the same image, with the same masks and read-only `.git/hooks`, `.git/config`, `.envrc` and `kota.yaml` as `run`, but on a normal network so registries are reachable, and without an agent, the forwarder or the gateway key. Run it again after changing a manifest. When the agent needs a new dependency, add it to the manifest on the host and re-run `prep`. If a step fails, its output is followed by a line naming the step, its command and exit status, e.g. `kota: install step npm failed (exit 1): npm ci`.

What it runs is detected from the project root:
- `package.json`: pnpm, yarn or npm, chosen by the `packageManager` field, else by lockfile (`pnpm-lock.yaml`, `yarn.lock`, otherwise npm). pnpm and yarn come from corepack. `packageManager` must be a plain version (`pnpm@10.0.0`, optionally `+sha512.…`), not a URL. With a lockfile, the install is frozen (`pnpm install --offline --frozen-lockfile`, `yarn install --frozen-lockfile` or `--immutable`, `npm ci`).
- `go.mod`: `go mod download`, in every module directory, or once at the root with a `go.work`. Keep `go.sum` complete (`go mod tidy` on the host).

To run something else, put the command in `kota.yaml` at the project root:

```yaml
prep:
  command: pnpm install && pnpm build   # replaces the detected installs; run by sh in /workspace
  dirs: [.venv]                         # extra dependency directories (see below)
```

The command runs with network access, so `kota.yaml` is read-only inside the sandbox, like `.kotaignore`. The detected fetches (below) still run before it.

Dependency directories (`node_modules` at the root and in each workspace package, plus `prep.dirs`) are Docker volumes per project, `kota-deps-<hash of the project path>-<hash of the directory>`, mounted over those paths in `prep`, `run` and `check`. Linux binaries never land in your project directory. A `node_modules` you have on the host stays untouched and isn't visible in the sandbox. A dependency directory may not be or contain a protected or masked path, or lie inside one, and may not go through a symlink. `prep` deletes and recreates these volumes each time, so nothing an agent session wrote into them runs during the install. It fails while a session of the project is running.

If the dependencies can't be worked out, for example because an agent left `package.json`, `pnpm-workspace.yaml` or `kota.yaml` unparseable or set an unsupported `packageManager`, `prep` fails, but `run`, `shell` and `check` warn and start anyway so you can fix the file. They mount the dependency directories of the last successful `prep` (only the root `node_modules` if the project was never prepped), plus a volume over every other `node_modules` found on the host, so no host `node_modules` is visible in the sandbox. Run `prep` once the file is fixed.

Caches are shared by all projects: the Go module cache (`kota-cache-gomod`, `GOMODCACHE`), corepack's pnpm and yarn releases (`kota-cache-corepack`), and the pnpm store (`kota-cache-pnpm-store`). So that no project can plant code in another project's dependencies, only package manager code ever writes them. `prep` runs in two phases:
1. Fetch, with the caches writable: `corepack install`, `pnpm fetch --ignore-pnpmfile` and `go mod download`. No project code runs: no lifecycle scripts, pnpmfile or `yarnPath`. What is downloaded is checked against the lockfile, `go.sum` or the `packageManager` hash, and the caches are keyed by content or version.
2. Install, with the caches read-only: `pnpm install --offline`, `yarn install`, `npm ci` and the `kota.yaml` command, which run lifecycle scripts.

pnpm without a lockfile has nothing to fetch ahead, so its install uses a store of its own for that run; the next `prep` then uses the shared one. `run` mounts the Go module and corepack caches read-only, and sets `GOPROXY=off` and `COREPACK_ENABLE_NETWORK=0` so a missing dependency fails at once. `run` and `check` print a hint when a dependency directory is empty though its manifest exists.

The agent can write the files that decide what `prep` runs, and `prep` runs them with network access. So `prep` asks you to type `y` first when:
- it is the project's first `prep` (it lists the commands and the project code the install runs);
- a file that decides what prep runs changed since the last successful `prep`: `package.json` files, lockfiles, `pnpm-workspace.yaml`, `.pnpmfile.cjs`, `.npmrc`, `.yarnrc.yml`, `.yarn/releases/`, `.yarn/plugins/`, patches, `go.mod`, `go.sum`, `go.work` or `kota.yaml`;
- the install runs project code (lifecycle scripts like `postinstall` or `prepare`, a pnpmfile, a `yarnPath`, the `kota.yaml` command) and any other project file changed, since that code may run it. Lifecycle scripts count in the root and workspace packages, and in local dependencies (`file:`, `link:`, `portal:` or a relative path inside the project, and theirs in turn) listed in `dependencies`, `devDependencies` or `optionalDependencies`. A local tarball dependency and any git dependency (`github:user/repo`, `user/repo`, `git+https://...`) count too.

It lists what changed; review it (`git diff`) before you answer. Anything else, or no terminal, aborts. The state after each `prep` is recorded in `~/Library/Caches/kota/prep/` (the user cache dir), outside the sandbox.

Limits:
- `prep` has the same network as any container: the internet, the LAN and the host's loopback services (including the gateway port, but without the key). It runs no containment probes.
- Changes in dependency and build directories (`dist`, `build`, `vendor`, ...) don't count for the confirmation, as for the change report. Don't let install scripts run files from there.
- The confirmation is only as good as your review. A dependency from the registry can still run its own install scripts during its project's `prep`, with the masks in place.
- Install scripts can't change `.git/config`, so a `prepare` script that sets `core.hooksPath` (husky) fails or does nothing; run it on the host.
- `.npmrc` is masked by default, so registry settings and tokens in it don't apply. Unmask it in `.kotaignore` (`!.npmrc`) if `prep` needs it; the agent can then read it too.
- `prep` reports changed files that run things, like `run`.

## Cleanup

`kota clean [dir]` lists the project's existing agent state and dependency volumes, then asks you to type `y`. Declining or reaching end of input leaves them in place. It removes state for both agents and dependency volumes for directories no longer mentioned in the current configuration. The selected volume names must match the exact hash of the resolved project path. It also resets the host's prep confirmation record, so the next `prep` asks for approval again, removes the project's Engine VM data (see [Engine VM](#engine-vm)) and the copies kept after `--no-history` sessions, with any work in them that wasn't brought back. When there are no volumes, prep record or engine data, it reports that there is nothing to clean.

Cleanup holds the project's session lease through listing, confirmation and removal. It refuses while a run, shell, prep or check is using that project. It works without a model gateway and without parsing the project's dependency configuration, so it can clean up after manifests or configuration have been removed or broken. After cleanup, `run` creates fresh state and empty dependency volumes; run `prep` again to restore dependencies. Project images and source files are preserved.

Shared caches are retained by default. `kota clean --shared-caches [dir]` includes the Go module cache, corepack releases and pnpm store in the confirmed list. It requires exclusive access: any starting, running or finishing session prevents the reset, and a reset in progress prevents new sessions from starting. The cache reservation covers confirmations and gaps between prep containers too. Other projects keep their own state and dependency volumes, but need `prep` again to refill shared caches. Docker refuses removal of a volume still attached to a container. If listing or removal fails, clean reports the error and retains the prep confirmation record; a removal error can occur after some of the listed volumes were already deleted.

## Engine VM

Some projects' tests start containers, for example testcontainers with Postgres. The sandbox has no Docker of its own, and never gets the host's: controlling a Docker engine is root on the machine that runs it. A project that needs containers declares the images in `kota.yaml` instead, and gets its own **Engine VM**: a small QEMU VM, managed by kota, that runs only the containers started in its sessions.

```yaml
engine:
  images: [postgres:16-alpine]   # plain references with an optional tag
  cpus: 2                        # optional, default 2
  memory: 2G                     # optional, default 2G; M or G (MiB or GiB), at least 512M
```

`images` is required and lists each image once. Each must be a valid Docker reference with a tag or none (`latest`), never a digest: prep records the digest the tag resolves to. `cpus` is a whole number of at least 1. The VM runs next to your model, so keep it small. An invalid reference, a size without its unit or out of range, or an unknown key in the section is rejected with a message naming it: `prep` refuses, and so do `run` and `shell` once the project has been prepped with an engine (see below). `kota.yaml` is mounted read-only in the sandbox, so the agent can't declare images or resize the VM.

`kota prep` downloads the images with crane, after the dependency fetch and with no project code running: crane runs in a throwaway container of the base image that has only the project's new archive directory mounted, not the project. Each tag is resolved to its digest and the image pulled by that digest, so every manifest and layer is checked against it. The archives and the digests they were pulled by are kept in `kota/engine/projects/<hash of the project path>/images/` under the user cache dir, outside the project and the sandbox.

Each later `prep` first looks up, with crane in the same kind of container, which digest each tag of the last prep points to now. It looks up only the images the last prep recorded: an image newly declared in `kota.yaml`, which an agent may have written, isn't sent anywhere until you confirm. Then prep asks you to type `y` when the images changed, even if no file did, listing them:

```
Engine images, downloaded with crane (no project code runs):
  new      redis:7
  moved    postgres:16-alpine: sha256:3f5a9c1e2b7d -> sha256:9b04e8d2c6a1
  removed  nginx:1.27
```

Only new images, images whose tag moved (pulled by the digest shown) and images whose archive is gone are downloaded; the others are kept as they are. A failed lookup or download names the image and keeps the previous archives.

`kota run` and `kota shell` start the project's Engine VM, for opencode and pi alike, wait for its Docker API, load the archives, and point the sandbox at it:

- `DOCKER_HOST=tcp://engine:2376`, `DOCKER_TLS_VERIFY=1` and `DOCKER_CERT_PATH=/etc/kota/engine`, so the Docker CLI (in the base image) and testcontainers use it;
- `TESTCONTAINERS_HOST_OVERRIDE=engine`, so tests connect to their containers' published ports through the same route;
- `TESTCONTAINERS_RYUK_DISABLED=true`. Ryuk would need the Docker socket mounted into a container, and the VM is reset every session anyway.

The tests need no changes. The VM stops when the session ends, including on Ctrl-C, SIGTERM or a closed terminal. If kota itself is killed outright, the next command for the project stops the VM, found through its pidfile. `kota check` starts it too: it lists the declared images with the digests prep recorded, says why the VM can't start if it can't, and shows the Engine VM probes after the sandbox's. Projects without an `engine:` section start no VM and behave as before.

The agent is told whether it has the Engine VM, so it doesn't claim its container tests passed when they couldn't run. In an engine project, `KOTA_ENGINE` is `available` or `unavailable`, and a matching note is added at the end of the agent's instructions (see [What the agent is told](#what-the-agent-is-told)). pi reads a project's `.pi/APPEND_SYSTEM.md` instead only in trusted projects, and kota trusts none.

The VM never downloads an image. A test or `docker` command that needs an image not declared under `engine.images` fails when Docker tries to pull it, with Docker's own error, a failed lookup of the registry (`lookup registry-1.docker.io: Temporary failure in name resolution`), within seconds. Nothing reaches a registry. The Docker CLI names the image first (`Unable to find image 'redis:7' locally`); testcontainers' error doesn't. That error doesn't mention kota, so the agent's note lists the declared images, explains what such a failure means, tells the agent to find the image in the failing test when the error doesn't name it, and not to retry or work around it. It should report the affected tests as unable to run, and ask you to add the image to `kota.yaml` and run `kota prep`.

`--no-engine` on `run` or `shell` starts the session without the Engine VM on purpose, for work that doesn't need containers, or when the VM can't start (no QEMU, no base disk, images not prepped). No VM, engine forwarder, certificates or `DOCKER_HOST` are set up, startup says so, and the agent is told that tests needing containers can't run and must be reported as not run, never as passed. In a project without an `engine:` section, `--no-engine` changes nothing, and no variable or Engine VM note is added.

Containment, set from outside the VM, since the agent has root inside it:

- **No route out.** The VM uses QEMU's user-mode networking with `restrict=on`, so the guest can't open connections to the internet, DNS servers, the host or the LAN, even as root with its own routes. It never has a route out, not even during prep. Images come in only as the archives prep downloaded.
- **Only the agent's ports.** QEMU forwards only the Docker API and the published port range (40000-40099, set as the VM's `ip_local_port_range`, so Docker publishes container ports only there), each from a free port on the host's `127.0.0.1`. An engine forwarder container, like the gateway's, relays them to the sandbox's internal network as `engine`, with the VM's own port numbers.
- **Only this session's client.** The forwards listen on the host's loopback, which any container can reach, so dockerd requires TLS with a client certificate. Each session makes a new CA, server and client certificate (EC P-256) in its session directory. The server side reaches the VM through QEMU's `fw_cfg` and is read at boot, so the shared disk holds no keys. Only the client side is mounted, read-only, into the sandbox, and the probes check that it rejects writes. A certificate from another session or project, or none, is refused.
- **The route dies with the VM.** If the VM stops during a session (the agent can power it off as root), kota kills the engine forwarder at once, so the agent can't reach whatever binds the freed host ports next.
- **Limit: bursts of new connections.** QEMU's port forwards accept only a few pending connections at a time, so in a burst of parallel new connections to one port some are cut off during the TLS handshake (8 of 40 in a test). Sequential connections and kept-alive ones aren't affected. The probes run their API checks one at a time for this reason.
- **Limit: published ports have no authentication.** Like the API, the published-port forwards listen on the host's `127.0.0.1`, which other containers can reach, for example another project's `prep`. Only the API requires the certificate; a container the agent publishes (a test database) is as open to them as its own credentials make it.
- **Nothing carries over.** Every session boots from the **Engine base disk** through `-snapshot`, a throwaway layer, so files, containers and images from the last session are gone. The VM has no host directories mounted.
- **The sandbox stays as it was.** The agent container is still unprivileged with all capabilities dropped, on the internal network only, and without the host's Docker socket.

The VM boots its kernel directly, without firmware or a boot loader. With `postgres:16-alpine` it is ready in about 5 seconds, booting while the sandbox starts. It has the `cpus` and `memory` of the engine section, 2 CPUs and 2 GB by default.

`kota build` also builds the Engine base disk, once, in `kota/engine/base/` under the user cache dir. It downloads the Debian 13 cloud image and checks it against its pinned SHA-512, then runs one online boot that installs Docker from Debian, sets up the port range and the TLS service, disables cloud-init and powers off (about 1.5 minutes). That boot gets its cloud-init seed from a server on the host's loopback, which also receives the kernel, initrd and root partition for the direct boot. It runs no project code and isn't any project's Engine VM. Later builds skip it while it is up to date; sessions warn when the disk is from an older definition. Without QEMU, `build` skips the disk with a note.

Before every `run` and `shell` with the Engine VM, after the sandbox's probes, the VM is probed too:

- From a probe container inside the VM, on the VM's own network, so with everything root in the VM could reach: the internet over IPv4 and IPv6, DNS (the VM's resolver, QEMU's DNS relay `10.0.2.3` and `1.1.1.1`), and the host through QEMU's host alias `10.0.2.2`, the host's own addresses and the LAN gateway, on the gateway's port, the usual service ports and the host side of the VM's own forwards. All must fail. A connection to the VM itself must succeed, so a broken probe container can't pass for a contained VM. A must-fail probe whose command is missing counts as failed, here and in the sandbox's probes.
- From the sandbox: the Docker API must answer with the session's certificate and refuse a client without one, and a port published by a probe container in the VM must answer through the engine route.

The VM can't download anything, so the probe image is the forwarder image (alpine with socat), saved from the host's Docker and loaded for the probes only; it and the probe container are removed afterwards. A failed probe blocks the session like a sandbox probe.

A `run` or `shell` of an engine project refuses to start, with the fix, when QEMU isn't installed, the Engine base disk isn't built (`kota build`), a declared image hasn't been prepped (`kota prep`), the VM doesn't boot to a ready Docker API within 60 seconds, or an Engine VM probe fails. `--no-engine` starts it anyway, without the VM; `--unsafe-skip-check` skips the Engine VM probes along with the sandbox's. If `kota.yaml` can't be parsed, a project that was prepped with an engine refuses too, since its images aren't known; other projects start as before.

All engine data lives under `kota/` in the user cache dir, never in the project: the shared Engine base disk in `engine/base/`, and each project's image archives in `engine/projects/<hash>/`, next to the running VM's pidfile. A VM leaves no disk behind: `-snapshot` keeps its writes in a temporary file QEMU deletes. Its certificates and console log are in the session directory under `sessions/`, removed when the session ends. `kota clean [dir]` lists the project's engine directory with its archived images, and any session directories left by sessions that didn't end cleanly (they may still hold certificates), and removes them with the volumes after you type `y`. The next `prep` then downloads the images again. The shared base disk is kept; `kota build` replaces it when its definition changes.

Requirements: QEMU (`brew install qemu` on macOS; it ships the UEFI firmware the base disk build uses). The setup uses only QEMU features that Linux has too, with KVM instead of macOS's HVF, but it is so far only tested on macOS on Apple silicon.

## Project image

A project that needs more than Node and Go can add a `kota.Dockerfile` at its root, on top of the base image:

```dockerfile
FROM kota-base:latest
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*
```

`kota build dir` builds it as `kota-project-<hash of the project path>`, and `prep`, `run`, `shell` and `check` of that project then use it (`check` shows the image). Projects without the file use the base image. The build runs as root with network access, like any `docker build`, but with an empty build context, so `COPY` and `ADD` can't read the project or its masked secrets. Download what you need in a `RUN`, or copy it from another image (`COPY --from=...`). The file is read-only inside the sandbox, like `kota.yaml`, so the agent can't change what the next build runs. A symlinked `kota.Dockerfile` is refused.

The image is labeled with the Dockerfile's hash and the base image it was built on. When the Dockerfile has changed or `kota build` has rebuilt the base image since, sessions warn and use the old image. Without an image yet, they warn and use the base image. Each warning shows the `kota build dir` command that fixes it. Run it again to pick up a new base image even when the Dockerfile is unchanged. The sandbox sets the user, capabilities and networks, so `USER` or `EXPOSE` in the Dockerfile don't change them. The root container that prepares dependency volumes always uses the base image.

### Rust

Copy the toolchain from the official image, the way the base image gets Go, and keep Cargo's downloads and build output in dependency volumes:

```dockerfile
FROM rust:1-slim-trixie AS rust

FROM kota-base:latest
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo
# The toolchain stays in the image; the registry and git checkouts go in
# the .cargo-home dependency volume.
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/workspace/.cargo-home \
    PATH=/usr/local/cargo/bin:$PATH
```

```yaml
# kota.yaml
prep:
  command: cargo fetch
  dirs: [.cargo-home, target]
```

Pin the `rust` tag to a version for reproducible builds. `cargo fetch` needs a `Cargo.lock`. In `run`, `cargo build` and `cargo test` then work offline; add `--offline` if Cargo tries the network anyway.

To make Rust available to every project instead, add the same `COPY` and `ENV` lines to `assets/images/base/Dockerfile`, and, for a registry cache shared across projects like the Go module cache, a `CARGO_HOME` entry in `sandbox.sharedCaches` with `cargo fetch` in the fetch phase.

## Tests

Integration tests need Docker: `go test -tags integration ./...`. The prep and project image tests also need network access. The Engine VM tests also need QEMU and the Engine base disk (`kota build`), and are skipped without them.
