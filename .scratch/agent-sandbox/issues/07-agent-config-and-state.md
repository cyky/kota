# 07 — Agent config from repo and per-project state volumes

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- opencode base config (permissions, defaults) versioned in this repo and mounted read-only into the sandbox.
- Agent state (sessions, logs, caches) stored in a Docker volume per agent per project, named from a hash of the resolved project path.
- Host `~/.config/opencode` (or any host agent config) is never mounted.
- Sessions can be resumed in a later `run` of the same project.

## Acceptance criteria

- [ ] The agent can't modify its config inside the sandbox
- [ ] A session started in project A can be resumed in a later `run` of project A
- [ ] Project B can't see project A's sessions
- [ ] Volume names are stable for the same path and differ across paths (unit test on SandboxSpec)
- [ ] No host agent config directory appears in the mounts (unit test)

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
