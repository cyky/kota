# 15 — SessionRegistry: one session per project, model-clash warning

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **SessionRegistry** module (`Acquire(project, agent, model) → lock | busy`, `Active() → sessions`) used by `run`, `shell` and `prep`.

- Only one session per project at a time; a second attempt is refused and names the running session.
- Different projects can run at the same time.
- Stale locks from crashed sessions are detected (dead PID / container gone) and cleaned up.
- When a new session uses a different model than a running session, warn that llama-swap will swap models back and forth.

## Acceptance criteria

- [ ] Two `run`s on the same project → second refused
- [ ] `run`s on two different projects → both start
- [ ] After killing a session abruptly, the next `run` on that project starts
- [ ] Different-model sessions → warning shown
- [ ] Unit tests for acquire/busy/stale/clash

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
