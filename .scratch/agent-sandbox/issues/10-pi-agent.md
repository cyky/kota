# DONE — 10 — pi agent support

Status: done
Resolved in: `f5a45dc`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- Install `@earendil-works/pi-coding-agent` in the base image, pinned, with `npm install -g --ignore-scripts` (requires Node ≥ 22.19).
- `run --agent pi` starts pi in the sandbox.
- ModelCatalog renders pi's provider config pointing at the in-sandbox gateway.
- pi state in its own per-project volume; pi base config read-only from the repo.
- pi has no permission system, so it's always treated as full auto: the dirty-tree `y` confirmation applies, and startup states that the sandbox is pi's only boundary.

## Acceptance criteria

- [x] `localllm run --agent pi` starts pi and it can complete a prompt via the gateway
- [x] pi sessions persist per project and are isolated between projects
- [x] Dirty tree → `y` confirmation before pi starts
- [x] Probes run before pi sessions, same as opencode
- [x] Unit tests for the pi config rendering

## Blocked by

- `07-agent-config-and-state.md`
- `08-model-catalog.md`
- `09-approval-modes.md`

## Comments

**2026-10-14 — Done.** Implemented in `f5a45dc` on `master`.

- Base image: `@earendil-works/pi-coding-agent@1.2.0` with `npm install -g --ignore-scripts` (Node 24.21).
- `--agent opencode|pi` on `run` and `check`; an unknown name is refused with the list. Each agent's config files, env and default command are set up in `agentFor` in `cmd/localllm/main.go`.
- `internal/pi`: `RenderSettings(base, Settings)` sets `defaultProvider`/`defaultModel` over `assets/agents/pi/settings.json` (`defaultProjectTrust: never`, install telemetry off, built-in llama.cpp extension off). `RenderModels` renders the gateway as the only provider (`openai-completions`, placeholder API key, context and max tokens per model). `Env` sets `PI_CODING_AGENT_DIR`, `PI_CODING_AGENT_SESSION_DIR`, `PI_OFFLINE`, `PI_SKIP_VERSION_CHECK`, `PI_TELEMETRY=0`.
- Config: `settings.json` and `models.json` are mounted read-only into `~/.pi/agent`. A fully read-only agent dir breaks pi (it creates `settings.json.lock` next to the file), so the dir is its own tmpfs (`sandbox.Inputs.Scratch`), discarded after the session. The mounted files can't be moved away (EBUSY).
- State: sessions in `localllm-state-pi-<hash>` at `/var/lib/localllm/state/pi/sessions`.
- pi is always full auto: the dirty-tree confirmation applies, and startup says the sandbox is its only boundary.

Findings in pi 1.2.0:
- pi reads a project `.pi/settings.json` `sessionDir` before project trust; `PI_CODING_AGENT_SESSION_DIR` outranks it.
- With `defaultProjectTrust: never`, a project `.pi/APPEND_SYSTEM.md` is not loaded; with `pi --approve` it is. A user's own `.pi/` is therefore not used in the sandbox. `AGENTS.md` still loads.
- pi offers a provider's models only when it has an API key, hence the placeholder.

Verified:
- Unit tests: settings and models rendering, shipped base settings, env, scratch tmpfs in the spec, mounts/env/command/state volume for both agents, unknown agent, `--agent` parsing. Integration test: read-only config files inside a scratch tmpfs, lock dir creatable, files can't be moved, nothing persists between containers. Full `-tags integration` suite passes.
- Real llama-swap in throwaway projects under `~/Library/Caches`: `pi -p` replied after 64 probes passed; with `qwen3.6-coder` pi created a file via tools; a later run of A with `pi -c` recalled a word from the earlier session, B had no pi sessions; dirty tree with `n` exits 1 and creates no containers; project `.pi/` ignored as above; `check --agent pi` probes both config files read-only; opencode still works.

Not verified: the interactive pi TUI. Notes: pi has no per-model tool-call flag, so `tools: false` models are offered too; `check` still tags the small model, which pi doesn't use.
