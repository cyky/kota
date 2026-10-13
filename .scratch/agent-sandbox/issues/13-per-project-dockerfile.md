# 13 — Per-project `localllm.Dockerfile`

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

If a project contains `localllm.Dockerfile`, `localllm build [dir]` builds a project image on top of the base image, and `prep`/`run`/`shell` use it for that project.

- The project image is tagged per project (hash of the resolved path).
- Rebuild when the Dockerfile changes; `run` warns if the image is stale or missing.
- Document how to add Rust this way (and as a base layer later).

## Acceptance criteria

- [ ] A project Dockerfile that installs an extra apt package results in that package being available in `run`
- [ ] Projects without the file use the base image
- [ ] Stale/missing project image → warning with the command to fix it
- [ ] Unit test: SandboxSpec picks the project image when present

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
