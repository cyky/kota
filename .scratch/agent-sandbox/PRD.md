# PRD: Offline agent sandbox (`localllm`)

Status: ready-for-agent

## Problem Statement

I want to use coding agents (opencode, pi) with my local models, which are served by llama-swap on my Mac. But I don't want to give an agent my whole machine. Run natively, an agent can read any file my user can read: SSH keys, `.env` files, other projects. It can reach the internet, so data could be sent out or untrusted code pulled in. And it can plant things that later execute outside its control: git hooks, git config, direnv `.envrc`. pi has no permission system at all.

I want an agent to work on one specific project directory, with no internet access, while still reaching my local model gateway. I want enough confidence in that containment to eventually let it run without approval prompts.

## Solution

A Go CLI, `localllm`, that runs an agent inside a Docker container (Rancher Desktop). The sandbox has these properties:

- **No internet.** The agent container sits on an internal Docker network with no route out. A separate forwarder container is the only bridge, and it exposes exactly one destination: the llama-swap gateway on the host.
- **One project, mounted read-write, plus optional read-only extras.** Nothing else from the host exists inside. Dangerous mount targets are refused.
- **Secrets masked.** Files matching a global ignore list plus a per-project `.localllmignore` are replaced by empty read-only mounts.
- **No host code execution via the project.** `.git/hooks`, `.git/config` and `.envrc` are read-only inside the sandbox. After the session, the CLI warns if files that run things changed.
- **Dependencies installed separately.** `prep` installs project dependencies with network and no agent. `run` is the offline agent session. Dependency directories live in Docker volumes, so Linux binaries never mix with macOS ones.
- **Verified every time.** Before every session, containment probes run inside the real sandbox. Any failure blocks the session.
- **Models discovered automatically.** The model list is read from the gateway's `/v1/models` and combined with a small `models.yaml` for metadata.

Commands: `build`, `prep`, `run`, `shell`, `check`, `clean`.

## User Stories

### Starting sessions

1. As a developer, I want to run `localllm run` in a project directory, so that an agent starts there in an offline sandbox without further setup.
2. As a developer, I want to pass a directory explicitly (`localllm run ~/Projects/foo`), so that I can start a session for a project other than the current directory.
3. As a developer, I want to choose the agent with `--agent opencode|pi`, so that I can use either agent with the same sandbox.
4. As a developer, I want opencode to be the default agent, so that sessions start with approval prompts unless I opt out.
5. As a developer, I want to pick a model with `--model <id>`, so that I can try a different local model for one session.
6. As a developer, I want `qwen3.6-coder` to be the default model, so that I don't need to pass a model every time.
7. As a developer, I want `localllm shell` to give me a plain shell in the same offline sandbox, so that I can inspect the environment or run commands exactly as the agent would.
8. As a developer, I want to resume an earlier agent session in the same project, so that work can continue across days.

### Network containment

9. As a developer, I want the agent unable to reach any internet address, so that it can't send out code or secrets or download untrusted content.
10. As a developer, I want the agent unable to resolve external DNS names, so that DNS can't be used as an exfiltration channel.
11. As a developer, I want the agent unable to reach other services on my Mac's loopback (via `host.docker.internal` / the VM gateway), so that only the model gateway is reachable from the sandbox.
12. As a developer, I want the agent unable to reach my LAN, so that devices on my network are not exposed.
13. As a developer, I want the agent to reach the model gateway at a stable in-sandbox address, so that agent config doesn't depend on host networking details.
14. As a developer, I want the network rules enforced outside the agent's container, so that the agent can't disable them even if it gains root inside its container.
15. As a developer, I want to configure the gateway URL (default `http://host.docker.internal:8080`), so that I can move llama-swap to another port later.
16. As a developer, I want an optional gateway API key passed into the sandbox, so that I can enable llama-swap `apiKeys` without changing the tool.
17. As a developer, I want hosted provider keys to never enter the sandbox, so that adding llama-swap peers later doesn't expose those keys to the agent.

### Filesystem containment

18. As a developer, I want only the selected project mounted read-write at `/workspace`, so that the agent can't touch my other files.
19. As a developer, I want to add read-only reference directories with `--ro PATH` (repeatable), so that the agent can read specs or docs without modifying them.
20. As a developer, I want the launcher to refuse `$HOME`, `/` and paths outside a configured allowed root, so that a typo can't expose my whole machine.
21. As a developer, I want symlinks resolved before path validation, so that a symlink can't smuggle in a forbidden path.
22. As a developer, I want `$HOME`, `~/.ssh`, `~/.config` and the Docker socket never mounted, so that credentials and container control stay out of reach.
23. As a developer, I want the agent to run as my UID/GID, so that files it creates belong to me on the host.
24. As a developer, I want the container to run without sudo, with all capabilities dropped and `no-new-privileges`, so that the agent has as little privilege as possible.

### Secret masking

25. As a developer, I want a built-in default ignore list (`.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, `.npmrc`, `.netrc`, `.aws/`, and similar), so that common secrets are hidden without configuration.
26. As a developer, I want a per-project `.localllmignore` in gitignore syntax, so that I can hide project-specific sensitive files.
27. As a developer, I want masked files to read as empty and be read-only inside the sandbox, so that the agent can neither read nor overwrite them.
28. As a developer, I want masked directories replaced by empty read-only directories, so that whole secret folders are hidden.
29. As a developer, I want a warning when a masked file is tracked in git, so that I don't accidentally let the agent commit an emptied file.
30. As a developer, I want the mask scan to skip heavy directories such as `node_modules`, so that launch stays fast.
31. As a developer, I want to know that masks are computed at launch, so that I understand files created on the host mid-session aren't masked.

### Host-execution protection

32. As a developer, I want `.git/hooks` read-only inside the sandbox, so that the agent can't plant hooks that run when I use git on the host.
33. As a developer, I want `.git/config` read-only inside the sandbox, so that the agent can't set `core.hooksPath`, `core.fsmonitor`, `core.sshCommand` or aliases that execute on the host.
34. As a developer, I want `.envrc` read-only (with an empty placeholder if absent), so that direnv can't run agent-written code when I `cd` into the project.
35. As a developer, I want the agent to still commit, branch and diff, so that normal git workflows work in the sandbox.
36. As a developer, I want a warning after the session listing changed files that run things (`package.json`, `Makefile`, `.github/workflows`, `.vscode/`, `*.nix`, `flake.lock`), so that I know what to review before running anything on the host.

### Dependencies

37. As a developer, I want `localllm prep` to install project dependencies with network access and no agent, so that the offline agent session has what it needs.
38. As a developer, I want `prep` to detect the package manager (npm, pnpm, yarn via corepack; Go modules), so that I don't need to specify the install command for common setups.
39. As a developer, I want dependency directories (`node_modules`, `.venv`, `target/`, …) stored in per-project Docker volumes mounted over those paths, so that Linux and macOS binaries never mix in my project directory.
40. As a developer, I want the Go module cache and pnpm store in shared volumes, so that dependencies are reused across projects.
41. As a developer, I want `prep` to apply the same masks and read-only git/direnv mounts as `run`, so that install scripts can't read secrets or plant host-executed files either.
42. As a developer, I want `prep` to run without the gateway key, so that install scripts can't reach my models.
43. As a developer, I want the agent to tell me when it needs a new dependency, so that I can add it to the manifest and re-run `prep`.

### Images

44. As a developer, I want `localllm build` to build a base image with Debian slim, git, ripgrep, fd, jq, curl, build-essential, Node LTS with corepack, Go, opencode and pi, so that TypeScript and Go projects work out of the box.
45. As a developer, I want agent versions pinned in the image, so that builds are reproducible.
46. As a developer, I want pi installed from npm with `--ignore-scripts`, so that installing it doesn't run arbitrary lifecycle scripts.
47. As a developer, I want an optional per-project `localllm.Dockerfile` that extends the base image, so that unusual system dependencies can be added per project.
48. As a developer, I want Rust easy to add later (a base image layer or per-project Dockerfile), so that the design doesn't block new toolchains.

### Agent config and state

49. As a developer, I want agent config (providers, permissions, defaults) versioned in this repo and mounted read-only, so that the agent can't change its own permissions or providers.
50. As a developer, I want agent state (sessions, logs, caches) in a Docker volume per agent per project, so that sessions persist and one project's history isn't visible to another.
51. As a developer, I want my host `~/.config/opencode` and similar never mounted, so that hosted keys or internet-capable MCP servers don't leak into the sandbox.

### Approval modes

52. As a developer, I want opencode to ask for approval by default, so that I can watch what it does while I build trust.
53. As a developer, I want `--auto` to make opencode approve everything, so that I can let it work without interruption once I trust the sandbox.
54. As a developer, I want pi treated as always full-auto (it has no permission system), so that I understand the sandbox is its only boundary.
55. As a developer, I want a confirmation (type `y`) when starting a full-auto session (pi, or opencode with `--auto`) on a project with uncommitted changes, so that I can commit or stash before an agent can overwrite my work.

### Models

56. As a developer, I want the model list read from the gateway's `/v1/models` at launch, so that new llama-swap models appear without manual config.
57. As a developer, I want a `models.yaml` for metadata llama-swap doesn't report (context window, max output, tool-call support, agent visibility, default/small model), so that agents compact and behave correctly.
58. As a developer, I want models missing from `models.yaml` to appear with conservative defaults, so that they're still usable.
59. As a developer, I want models marked `agent: false` (e.g. `zeta2.1`) hidden from agents, so that agents don't pick models unsuitable for agent work.
60. As a developer, I want `qwen3.5-2b-instruct` configured as opencode's small model, so that titles and summaries don't swap out the main model.
61. As a developer, I want the generated provider config written read-only per session, so that it reflects the current gateway and the agent can't change it.

### Verification

62. As a developer, I want containment probes run inside the real sandbox before every `run` and `shell`, so that a Docker or Rancher Desktop update can't silently break containment.
63. As a developer, I want the probes to check that external IPs, external DNS, the host gateway address on any port and the LAN are unreachable, so that every egress route is verified as closed.
64. As a developer, I want the probes to check that writes to `.git/hooks`, `.git/config` and `.envrc` fail and that masked files read as empty, so that filesystem protections are verified.
65. As a developer, I want the probes to check that the gateway's `/v1/models` is reachable and `/workspace` is writable, so that I know the sandbox is usable, not just closed.
66. As a developer, I want a session to refuse to start if any probe fails (override only with `--unsafe-skip-check`), so that I never unknowingly run an agent in a broken sandbox.
67. As a developer, I want the probes to finish in about 1–2 seconds, so that checking every run doesn't slow me down.
68. As a developer, I want `localllm check` to show the resolved mounts, masks, refusals and probe results without starting an agent, so that I can inspect what a session would look like.

### Concurrency and cleanup

69. As a developer, I want sessions in different projects to run at the same time, so that I can work on several things in parallel.
70. As a developer, I want only one session per project at a time, so that two agents don't edit the same tree.
71. As a developer, I want stale locks from crashed sessions detected and cleaned up, so that a crash doesn't block a project forever.
72. As a developer, I want a warning when I start a session with a different model than an already-running session, so that I know llama-swap will keep swapping models.
73. As a developer, I want `localllm clean` to remove a project's state and dependency volumes, so that I can reset a project or reclaim disk.

## Implementation Decisions

### Architecture

- Docker via Rancher Desktop, driven with `docker compose`. No devcontainer; can be added later on top of the same compose spec.
- Two networks per session: an **internal** network (no route out) holding the agent container, and a normal network. The **forwarder** container (socat or Caddy) sits on both and forwards exactly one port to the gateway. The agent reaches the gateway at a fixed in-sandbox hostname (e.g. `gateway`).
- Verified on this machine: Rancher Desktop resolves `host.docker.internal` to `192.168.5.2`, which forwards to the host's `127.0.0.1`. So any container on a normal network can reach every host loopback service. That's why the agent must be on the internal network only.
- llama-swap is the only gateway. Hosting and configuring it is the user's job. Hosted providers would be added later as llama-swap `peers` with keys on the host. The CLI only needs a gateway URL and an optional key.
- Rejected alternatives: Apple `container` and Lima (egress control unclear or more work); Seatbelt/`sandbox-exec` (same kernel, deny-list model); a caching package proxy for offline installs (reopens egress); an in-container iptables firewall (requires `NET_ADMIN` inside the agent's container).

### Modules

Deep modules, pure or nearly pure, testable without Docker:

- **PathPolicy**: `Validate(project, roExtras, allowedRoot) → resolved paths | refusal`. Resolves symlinks. Refuses `$HOME`, `/` and anything outside the allowed root (default `~/Projects`). Refuses read-only extras that overlap forbidden locations.
- **MaskPlanner**: `Plan(projectDir, rules) → masks + warnings`. Combines global defaults and `.localllmignore` (gitignore semantics), walks the tree skipping heavy directories, emits file masks (empty read-only file) and directory masks (empty read-only directory). Also emits the read-only mounts for `.git/hooks`, `.git/config` and `.envrc` (placeholder when absent). Warns for masked paths tracked in git.
- **SandboxSpec**: `Build(session inputs) → compose spec`. Inputs: mode (`run` | `shell` | `prep` | `check`), agent, resolved paths, mask plan, model config, image tag, gateway URL/key. Produces services (agent, forwarder), networks, mounts, volumes and env. Per-project volume names derive from a hash of the resolved project path. Hardening: user is the host UID/GID, `cap_drop: ALL`, `no-new-privileges`. `prep` gets a normal network and no gateway key or forwarder; `run`/`shell` get the internal network plus forwarder.
- **ModelCatalog**: `Resolve(models from /v1/models, models.yaml, agent, modelOverride, auto) → agent provider config`. Merges discovered models with metadata, applies conservative defaults, hides `agent: false`, sets default and small model, renders opencode or pi config format, and sets opencode permissions (ask by default, allow with `--auto`).
- **ChangeReport**: `Snapshot(dir)` / `Diff(before, after) → warnings` over the list of files that run things.
- **SessionRegistry**: `Acquire(project, agent, model) → lock | busy`, `Active() → sessions`. One session per project; stale-lock detection; model-clash warning.

Shallow/integration modules:

- **Probes**: the containment probe suite, executed inside the sandbox; returns per-probe pass/fail. Must fail: external IP (`1.1.1.1:443`), external DNS, host gateway address on arbitrary ports, LAN gateway, writes to `.git/hooks`, `.git/config` and `.envrc`, reading masked content. Must succeed: gateway `/v1/models`, write in `/workspace`. Short timeouts so the suite takes about 1–2 s.
- **DockerDriver**: thin adapter over `docker compose` up/run/down, image build, and volume listing and removal.
- **CLI**: commands `build`, `prep [dir]`, `run [dir] [--agent] [--model] [--ro PATH]... [--auto] [--unsafe-skip-check]`, `shell [dir]`, `check [dir]`, `clean [dir]`. Wiring only.
- **Assets**: base image definition, opencode and pi base configs, default ignore list, `models.yaml`.

### Config and data

- Agent base config and `models.yaml` live in this repo, mounted read-only.
- Agent state: one volume per (agent, project).
- Dependency directories: per-project volumes mounted over the dependency paths. Shared volumes for the Go module cache and pnpm store.
- `models.yaml` fields per model: context window, max output, tool-call support, `agent` visibility; plus top-level default model (`qwen3.6-coder`) and small model (`qwen3.5-2b-instruct`). `zeta2.1` is `agent: false`.

### Agents

- opencode: default agent; asks for approval by default; `--auto` sets edit/bash permissions to allow.
- pi: `@earendil-works/pi-coding-agent` from npm, pinned, installed with `--ignore-scripts`; requires Node ≥ 22.19. No permission system, so always full auto.
- Full-auto sessions on a dirty working tree require typing `y`.

### Install

- `go install` from this repo. A Nix flake may come later.

## Testing Decisions

- Good tests check external behaviour through module interfaces: given inputs, assert on the returned plan, spec, config or warnings. They don't assert on internal helpers or call order. Tests should survive refactors that keep behaviour the same.
- **Unit tests (table-driven, no Docker)** for all deep modules:
  - PathPolicy: home/root/outside-root refusals, symlink escapes, nested and overlapping `--ro` paths.
  - MaskPlanner: default rules, `.localllmignore` patterns including negation and directory rules, heavy-directory skipping, tracked-file warnings, `.envrc` placeholder, git file mounts. Tests use temp directories with real files and real git repos.
  - SandboxSpec: the agent is only on the internal network; the forwarder exposes one port; no forbidden mounts appear; `prep` has no gateway key; hardening flags are present; volume names are stable for the same path and differ across paths.
  - ModelCatalog: merging, defaults for unknown models, `agent: false` hiding, `--model` override, opencode vs pi output, `--auto` permissions.
  - ChangeReport: detects added, modified and deleted watched files; ignores unrelated changes.
  - SessionRegistry: second acquire on the same project is refused; different projects succeed; stale lock recovery; model-clash warning.
- **Integration tests against real Docker** for Probes: build the real sandbox for a temp project and assert every must-fail probe fails and every must-succeed probe succeeds. Use a stub gateway (a tiny HTTP server serving `/v1/models` on host loopback) so tests don't depend on llama-swap. These tests are tagged and skipped when Docker isn't available.
- No CLI end-to-end tests; the CLI stays thin.
- Prior art: none. The repo is new. Use Go's standard `testing` package with table-driven tests.

## Out of Scope

- Running, configuring or exposing llama-swap (port, bind address, API keys, launchd). The user manages it.
- Hosted providers / llama-swap peers (OpenRouter, Anthropic, …).
- LiteLLM or any other gateway.
- Devcontainer / editor integration.
- macOS VMs and Seatbelt-based native sandboxing.
- Rust and other toolchains beyond Node and Go (to add later).
- A caching package proxy for installs during `run`.
- A model evaluation harness (e.g. fixed tasks run via `pi --print`).
- Nix flake packaging.
- Syncing a filtered copy of the project in and out (masking was chosen instead).

## Further Notes

- Open items for the user (not part of this work):
  - llama-swap currently listens on `0.0.0.0:8080` with no `apiKeys`, so it's reachable from the LAN. `--listen 127.0.0.1:8080` still works for containers.
  - `qwen3.6-coder` lacks `--jinja` in the llama-swap config. Verify tool calling works through the gateway. A tool-call probe could be added to `check` later.
- Model upgrade ideas for later: the same Qwen3.6-35B-A3B at Q6_K (about 28–30GB) for better tool-call reliability; or a larger MoE with few active parameters (80B–110B, Q3/Q4) if memory allows.
- Containment relies on Rancher Desktop's networking behaviour (`host.docker.internal` → host loopback, `--internal` networks). The per-run probes exist precisely because this can change between versions.
- Masks are computed at launch. Files created on the host mid-session aren't masked until the next session.
