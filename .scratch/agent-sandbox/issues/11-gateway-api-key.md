# 11 — Optional gateway API key passthrough

Status: ready-for-agent

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

- [ ] With llama-swap `apiKeys` enabled and the key configured, sessions work end to end
- [ ] With the key missing, the gateway probe fails with a clear message pointing to the key setting
- [ ] Unit test: `prep` spec contains no key; `run` spec does
- [ ] The key never appears in `check` output or logs

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
