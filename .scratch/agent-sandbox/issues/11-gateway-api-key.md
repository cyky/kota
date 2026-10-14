# DONE — 11 — Optional gateway API key passthrough

Status: done
Resolved in: `6fb99f2`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Support llama-swap `apiKeys` without changing the tool.

- The user provides a gateway key via environment variable or a file outside the repo (never stored in the repo).
- `run` and `shell` inject it into the agent's provider config / environment.
- The `/v1/models` fetch at launch uses the key too.
- `prep` never receives the key (enforced in SandboxSpec).
- No key configured → behaves as today.

## Acceptance criteria

- [x] With llama-swap `apiKeys` enabled and the key configured, sessions work end to end
- [x] With the key missing, the gateway probe fails with a clear message pointing to the key setting
- [x] Unit test: `prep` spec contains no key; `run` spec does
- [x] The key never appears in `check` output or logs

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `6fb99f2` on `master`.

- `internal/gatewaykey.Load`: `LOCALLLM_GATEWAY_KEY`, or `LOCALLLM_GATEWAY_KEY_FILE` (trimmed; empty, missing or both set are errors). Only printable ASCII without spaces, so the key can't add headers. Errors never contain the key.
- Spec: `sandbox.Inputs.Mode` (`ModeRun`, `ModePrep`) and `GatewayKey bool`. In run mode the agent service gets `Service.PassEnv = [LOCALLLM_GATEWAY_KEY]`, which `Service.MarshalJSON` writes as a null value. Compose then takes it from its own environment, so the key isn't in the compose file. Prep never gets it; the forwarder never does.
- `docker.Start(spec, dir, key)` puts the key in the environment of the session's compose commands only. `GatewayGet` passes it with `exec -e NAME` and expands it inside the forwarder for wget's `Authorization: Bearer` header. A 401 returns `docker.ErrUnauthorized`; `configure` adds a hint to set the variables (or check the key when one is set).
- Agent configs refer to the key by name: opencode `options.apiKey = "{env:LOCALLLM_GATEWAY_KEY}"`, pi `apiKey = "$LOCALLLM_GATEWAY_KEY"` (placeholder without a key). The gateway probe adds the header when the variable is set.
- `check` prints `gateway key: configured (value not shown)` or `none`.

Verified:
- Unit tests: key loading (env, file, both, missing, empty, spaces, CR/LF injection, key not in errors); spec per mode (run with and without key, prep with and without key) including the marshalled compose environment; rendered opencode and pi configs with and without key; a session built with a key has it by name only, and neither the compose spec nor the mounts output contains it.
- Integration test against a stub gateway that requires a key: right key gives the model list and a passing probe; no key or a wrong key gives `ErrUnauthorized` and a failing gateway probe. Full `-tags integration` suite passes.
- Real llama-swap v263: a second instance on 127.0.0.1:8081 with `apiKeys` (proxying to the user's instance) answers 401 without a key and accepts `Authorization: Bearer`. Through it: opencode (key from a file) and pi (key from env) replied; no key and a wrong key stopped `run` with the hint; `check` with the key passed every probe, without it the gateway probe failed with the hint in the models section. The key did not appear in `check` output or the llama-swap log. Without a key against the normal instance, pi still runs.

Not covered: `prep` (issue 12) and `shell` (issue 14) don't exist yet. The prep criterion is met through `ModePrep` in the spec; `shell` should pass the key like `run`. The key is visible inside the sandbox by design (the agent needs it) and in `docker inspect` of the agent container.
