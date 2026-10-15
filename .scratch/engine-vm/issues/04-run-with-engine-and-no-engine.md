# 04 — `localllm run` with the Engine VM, `--no-engine`, and the agent told

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

`localllm run` starts and stops the project's **Engine VM** around the **Session**, for opencode and pi, as `shell` does. `--no-engine` on `run` and `shell` starts a **Session** without it, on purpose. The agent's instructions and an environment variable say whether the **Engine VM** is available. When it isn't, the agent must report container **Gates** as not run instead of passed.

## Acceptance criteria

- [ ] `run` starts the **Engine VM** for engine projects, with both agents
- [ ] `--no-engine` skips the **Engine VM** on `run` and `shell`, and has no effect on projects without one
- [ ] The opencode and pi instructions state engine availability correctly in both states
- [ ] Unit tests for the agent config and SandboxSpec in both states
- [ ] README updated

## Blocked by

- `03-engine-probes-and-refusal.md`
