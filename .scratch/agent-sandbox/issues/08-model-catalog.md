# DONE — 08 — ModelCatalog: models from the gateway plus models.yaml

Status: done
Resolved in: `868bed0`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **ModelCatalog** module (`Resolve(discovered models, models.yaml, agent, modelOverride, auto) → agent provider config`) replacing the minimal config from slice 01.

- At launch, fetch `/v1/models` from the gateway.
- `models.yaml` in this repo holds metadata per model: context window, max output, tool-call support, `agent` visibility; plus top-level default model (`qwen3.6-coder`) and small model (`qwen3.5-2b-instruct`). `zeta2.1` is `agent: false`.
- Models missing from `models.yaml` appear with conservative defaults.
- `--model <id>` overrides the default; an unknown ID is refused with the list of available models.
- The generated config is written per session and mounted read-only.
- Render opencode format (pi format follows in slice 10; keep the renderer pluggable).

## Acceptance criteria

- [x] A new model added to llama-swap shows up in opencode without editing anything
- [x] `zeta2.1` is not offered to the agent
- [x] opencode uses `qwen3.5-2b-instruct` as its small model
- [x] `--model` selects a different model; an unknown ID errors helpfully
- [x] The generated config is read-only inside the sandbox
- [x] Table-driven unit tests: merging, defaults for unknown models, hiding, override, rendering

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `868bed0` on `master`.

- `assets/models.yaml`: default `qwen3.6-coder`, small `qwen3.5-2b-instruct`; context sizes from the llama-swap `-c` values (129024, 16384, 8192); `zeta2.1` is `agent: false`; `unknown` gives 8192 context, 2048 output, tools on. Unknown keys are errors.
- `internal/models`: `ParseFile`, `ParseDiscovered`, `Resolve(discovered, file, override) → Catalog`. Agent-neutral; `opencode.Render(base, Settings{GatewayURL, Models})` renders opencode's provider, `model` and `small_model`, so pi can add its own renderer in slice 10. `Resolve` has no `auto` parameter yet; permissions are issue 09.
- `/v1/models` is fetched with `wget` inside the running forwarder (`docker.Session.GatewayGet`), the agent's own route, because `host.docker.internal` doesn't resolve on the macOS host. The config is written after the fetch and before any agent container exists, and is mounted read-only as before.
- CLI: `--model` on `run` and `check`; `check` prints the resolved models. Flags may follow the directory, and `run -- cmd` without a directory no longer takes `cmd` as the directory.
- New dependency: `go.yaml.in/yaml/v3`.

Verified:
- Table-driven unit tests: merge, unknown-model defaults, duplicates, hiding, override, unknown and hidden override errors, default not served, small model fallback, parse errors, shipped file, rendering, argument parsing. Integration test: `GatewayGet` through the forwarder to the stub gateway. Full `-tags integration` suite passes.
- Real llama-swap: opencode's server API (`/api/model`, `/api/model/default`) lists exactly `qwen3.6-coder` and `qwen3.5-2b-instruct` with their limits and tool support, default `qwen3.6-coder`; `small_model` becomes the title agent's model. A real session: llama-swap had nothing loaded, then loaded `qwen3.5-2b-instruct` (title "PONG") and `qwen3.6-coder` (reply). `--model nope` and `--model zeta2.1` are refused with the available list.
- Stub gateway on host loopback serving an extra `brand-new-model`: it appears in opencode with the conservative values; `zeta2.1` doesn't.

Not verified: the model picker in the interactive TUI.

Notes: `opencode models` lists opencode's built-in hosted catalog, not the configured provider, so it can't be used to check this. The first API call in a fresh container can return that catalog too while the background service starts (as in issue 07).

Also found: docker compose expanded `$VAR` from the host environment in compose values. Fixed separately in `06f8f15` (see issue 01).
