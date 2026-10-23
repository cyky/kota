# The sandbox and how it protects the host

kota lets a coding agent work unattended on one project. It doesn't trust the agent: it assumes the agent may be confused, misled by a prompt in a file it reads, or simply wrong, and may try anything a shell allows. This page explains what the **Sandbox** stops, how, and what it doesn't stop. Each protection links to the page with its full details; [Architecture](architecture.md) has diagrams, and terms are as in `CONTEXT.md`.

## The threat model in one paragraph

The agent runs as a process inside a container, with a shell, and can write any file in the project. What we want to prevent is that it (1) reads or sends out anything outside the project, (2) reaches the internet, the LAN or services on the host, (3) changes anything on the host outside the project, or (4) leaves behind something that runs on the host later, when you build, open or commit the project. kota blocks (1) to (3) with the container and network setup, and blocks or reports (4).

## What the agent can and can't touch

| | Inside the sandbox |
| --- | --- |
| The project | Read-write at `/workspace`, except secrets, git hooks and config, kota's own files, and the Project skills when the project loads them |
| Secrets in the project (`.env`, keys, `.npmrc`, `.ssh/`, ...) | Masked: read as empty, can't be written |
| The project's git history | Readable, unless the session uses `--no-history` |
| `--ro` paths | Read-only at `/ro/<name>`, not masked |
| Your home directory, `~/.ssh`, `~/.config`, other projects | Not mounted at all |
| Host agent config (`~/.config/opencode`, ...) | Not mounted; the agent gets a config versioned in this repo, read-only |
| `HOME` | An empty tmpfs, thrown away when the session ends |
| The host's Docker socket | Never mounted |
| The network | Only the model **Gateway**, through one forwarded port |

## How the host is protected

### 1. Only the project is visible

The project directory is bind-mounted at `/workspace`; nothing else from the host is, apart from `--ro` reference paths you name and kota's own read-only config. The project must lie inside an allowed root (`~/Projects` by default), and kota refuses `$HOME`, `/` and the root itself, after resolving symlinks. `--ro` paths can't point at or around `$HOME`, the project, credential directories or Docker socket locations.

Agent state (sessions, logs, build caches) lives in a Docker volume per agent per project, so one project's sessions can't see another's.

Details: [Sessions](sessions.md#project-and---ro-paths).

### 2. The container has no privileges

The agent runs:

- as your UID, not root, named `agent` by a read-only `/etc/passwd` and `/etc/group` that kota writes for the session (so tools that look up the current user, such as Node's `os.userInfo()`, work);
- with all Linux capabilities dropped (`cap_drop: ALL`);
- with `no-new-privileges`, so setuid binaries can't raise it;
- unprivileged, and without the Docker socket, so it can't start containers on the host's engine.

So inside the container it can do what your user could do to the mounted files, and nothing more.

### 3. No route out, except to the model

The agent container is attached only to an internal Docker network:

- `internal: true` means the network has no route to anywhere.
- Isolated gateway mode means the Docker VM itself has no address on that network, so its own services (for example its sshd) can't be reached either.
- A small forwarder container (socat) sits on both the internal network and a normal one, and relays exactly one port to the **Gateway**. Inside the sandbox it is `http://gateway:8080`. For a **Gateway** that runs in a container, the forwarder joins that container's Docker network, named in your config, instead of the normal one.

The agent can't reach the internet, DNS, the LAN, the host's other ports or the Docker VM. It can't send your code anywhere, and it can't download anything. Dependencies are installed beforehand by `kota prep`, which runs without an agent.

Details: [Sessions](sessions.md#network).

### 4. Secrets are masked

At launch, kota finds secret files and directories in the project (`.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, `.npmrc`, `.netrc`, `.git-credentials`, Terraform state, `.aws/`, `.ssh/`, `.gnupg/`, ...) and mounts an empty read-only file or directory over each. Inside the sandbox they read as empty; on the host they stay untouched. Add your own rules in `.kotaignore`.

Masks hide files on disk only, not what git has stored: see the limits below.

Details: [Protecting the host](protections.md#secret-masks).

### 5. The agent can't plant code that runs on the host

The most indirect way to escape a sandbox is to write a file that the host runs later. kota closes the direct paths by mounting them read-only over themselves:

- `.git/hooks`, `.git/config`, `.git/commondir`, a project-local `core.hooksPath` (husky), submodules' hooks and config, and every file in the project that git config includes;
- `.envrc` (direnv);
- `kota.yaml`, `.kotaignore` and `kota.Dockerfile`, which decide what kota itself runs with network access or as root.

Missing ones get a read-only placeholder for the session, so the agent can't create them either. Each directory on the way to a protected path is pinned too, so it can't be renamed away and replaced. Git still works inside the sandbox.

The agent's own config can't come from the project either, since the agent could write one for its next session: kota renders it, and the agents ignore the project's `opencode.json`, `.opencode/`, `.pi/` and the like. The one exception is the project's skills in `.agents/skills`, which load when `kota.yaml` opts in, and are then read-only too unless you start the session with `--skills-writable` ([Project skills](sessions.md#project-skills)).

Files that run things but must stay editable (`package.json`, `Makefile`, `.vscode/`, `.github/workflows/`, `.agents/skills/`, `build.rs`, `.husky/*`, ...) can't be locked without breaking normal work. kota hashes them before the session and, when it ends, lists every one that was added, changed or deleted, so you can review them before you build or open the project. For a changed `package.json` or `go.mod` it names the dependencies and scripts that changed; for other files it prints the `git` command that shows the diff. It also lists the commits the session made, and warns about commits that touch masked files or hide a change to one of these files.

Details: [Protecting the host](protections.md).

### 6. Your uncommitted work is kept apart

Before a full-auto session with uncommitted changes, kota asks you to confirm, so the agent's work doesn't mix with yours. That check runs `git status` in a throwaway, offline container, not on the host, because host git could run a filter the agent configured. git never runs on the host against the project for kota's own checks and reports.

Details: [Sessions](sessions.md#approval).

### 7. Dependency installs are separated and confirmed

`kota prep` is the only step with internet access. It runs no agent, has no gateway key, and keeps the same masks and read-only files, except that it reads the project-root `.npmrc` for private registries, which sessions never see. Because the agent can edit the files that decide what `prep` runs (lockfiles, `package.json` scripts, `kota.yaml`), `prep` lists what changed since the last approved run and asks you to type `y`. Shared caches are written only by package managers in a phase where no project code runs, so one project can't plant code in another's dependencies.

Details: [Dependencies](dependencies.md).

### 8. Containers the agent starts run in their own VM

Tests that start containers (testcontainers) never get the host's Docker, because controlling a Docker engine is root on the machine that runs it. A project that declares images in `kota.yaml` gets an **Engine VM**: a QEMU VM with QEMU's restricted networking, so even as root inside it, the agent has no route out. Its Docker API needs a TLS client certificate made for this session only. It boots from a clean disk every session and has no host directories mounted.

Details: [Engine VM](engine-vm.md); why a VM: [ADR 0001](adr/0001-engine-vm-for-agent-containers.md).

### 9. Everything is checked before every session

Before every `run` and `shell`, probes run in a fresh container of the same setup. They try to reach the internet over IPv4 and IPv6, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN, and check that read-only mounts reject writes and that the gateway and workspace work. The Engine VM has its own probes. Any failure blocks the session. `kota check` shows the mounts, masks and probe results without starting an agent.

Details: [Sessions](sessions.md#containment-probes).

### 10. Nothing is left behind

Ctrl-C, SIGTERM or a closed terminal tears down the containers, networks, Engine VM, placeholders and session directory, and still runs the change report. Only one command at a time can use a project.

Details: [Sessions](sessions.md#interrupts).

## Drawbacks and limits

kota reduces risk; it isn't a guarantee. Know these before you point it at a project.

**The container boundary**

- **Containers share a kernel.** The sandbox is a container on the Docker VM (Rancher Desktop on macOS), not a VM of its own. A kernel or container runtime exploit would put the agent on that Docker VM, which mounts your home directory and can reach the host's loopback. On Linux, the shared kernel is the host's. Dropped capabilities and `no-new-privileges` make this harder, not impossible. Keep Docker and Rancher Desktop up to date.
- **No resource limits.** The agent container has no CPU, memory or process limits. A runaway build or fork bomb can slow down or exhaust the Docker VM, though not reach the host's files.
- **No approval with pi or `--auto`.** pi never asks before it edits a file or runs a command, and opencode doesn't with `--auto`. The sandbox is then the only boundary, and nobody sees a command before it runs.

**What the agent can still read or use**

- **Everything in the project that isn't masked.** Masks are found by name. A secret in an unusual file, or one created during the session, isn't masked. Dependency and build directories aren't searched.
- **Git history.** Masks don't reach `.git`. A secret that is or ever was committed can be read with `git log -p`. kota warns about tracked masked files but doesn't check history. `--no-history` hides the older commits but keeps the last one, with its files, message and author, so a secret tracked in it is still readable. To keep a path and its history away from the agent entirely, point kota at a directory that doesn't contain it, for example a subdirectory of a monorepo ([details](no-history.md)).
- **`--ro` paths** are mounted as they are, without masks.
- **The gateway key**, if you set one, is readable by the agent. Use a key meant only for the gateway.
- **The model.** The agent can use the gateway freely, including other models it serves.

**What the agent can still change**

- **The whole project.** It can delete or rewrite any unprotected file. Commit or stash first (full-auto sessions ask), and review the diff afterwards.
- **Files that run things are reported, not blocked.** `package.json` scripts, `Makefile`, `.vscode/`, CI workflows, husky scripts and similar are already changed on the host when you read the report. Review them before you build, install, or open the project in an editor that runs tasks.
- **Git config can point at writable scripts.** A protected config value like `core.fsmonitor` or a `filter.*` driver may name a project script, which stays writable. kota warns and reports changes to such scripts, but only when it can see the path in the command.
- **New git repositories.** The agent can `git init` a nested repository, or create bare-repository files at the project root; their hooks run when you use git there on the host. Set `git config --global safe.bareRepository explicit` to close the bare case.
- **Kept `--no-history` copies.** A copy kept after a session has git config and hooks the agent could write. Running git in it on the host runs them; look at them first, or remove the copy with `kota clean`.

**Other gaps**

- **`prep` has the network.** It reaches the internet, the LAN and the host's loopback, and runs install scripts, including those of registry dependencies, which can read the tokens in the project-root `.npmrc`. The confirmation is only as good as your review of what changed.
- **Engine VM ports.** Ports a test container publishes are forwarded to the host's `127.0.0.1` without authentication, so other containers on the machine (for example another project's `prep`) can reach them.
- **`--unsafe-skip-check`** runs the session even when a probe fails.
- **SIGKILL or a crash** skips the change report and leaves placeholders in the project (`git status --ignored` shows them). The protected and masked files stay locked on the host until the next kota command on the project.
- **Tested mainly on macOS** with Rancher Desktop on Apple silicon.

## In practice

- Commit before a session; review `git diff` and the change report after it.
- Keep secrets out of git history, and out of projects you run kota on, where you can. Where you can't, use `--no-history`, or point kota at a directory without them.
- Read the `prep` confirmation before typing `y`.
- Run `kota check` when you change the setup, and don't use `--unsafe-skip-check` as a habit.
