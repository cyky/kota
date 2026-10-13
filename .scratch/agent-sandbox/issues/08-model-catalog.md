# 08 — ModelCatalog: models from the gateway plus models.yaml

Status: ready-for-agent

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

- [ ] A new model added to llama-swap shows up in opencode without editing anything
- [ ] `zeta2.1` is not offered to the agent
- [ ] opencode uses `qwen3.5-2b-instruct` as its small model
- [ ] `--model` selects a different model; an unknown ID errors helpfully
- [ ] The generated config is read-only inside the sandbox
- [ ] Table-driven unit tests: merging, defaults for unknown models, hiding, override, rendering

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
