# DONE — 13 — Per-project `localllm.Dockerfile`

Status: done
Resolved in: `38474c0`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

If a project contains `localllm.Dockerfile`, `localllm build [dir]` builds a project image on top of the base image, and `prep`/`run`/`shell` use it for that project.

- The project image is tagged per project (hash of the resolved path).
- Rebuild when the Dockerfile changes; `run` warns if the image is stale or missing.
- Document how to add Rust this way (and as a base layer later).

## Acceptance criteria

- [x] A project Dockerfile that installs an extra apt package results in that package being available in `run`
- [x] Projects without the file use the base image
- [x] Stale/missing project image → warning with the command to fix it
- [x] Unit test: SandboxSpec picks the project image when present

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `38474c0` on `master`.

- `internal/projectimage`: tag `localllm-project-<32 hex of the resolved path>:latest`, `Read` (symlinks refused, whitespace-only counts as absent since it is a session's placeholder), labels with the Dockerfile sha256 and the base image ID, and `Choose`: project image when built; base image plus a warning when missing; project image plus a warning when the Dockerfile or base changed. Each warning names `localllm build <dir>`.
- `localllm build dir` builds with the Dockerfile on stdin and an empty context, so `COPY`/`ADD` can't read the project or masked secrets. It needs the base image. `prep`, `run` and `check` use the chosen image; `check` prints it, sessions note a project image.
- `localllm.Dockerfile` is protected by the mask layout (read-only, placeholder when missing), like `localllm.yaml`, so the agent can't change what a build runs with network. The probes cover it.
- `sandbox.Inputs.HelperImage`: the root `volumes` service always runs the base image, never a project image.
- README: "Project image" section with an apt example, a Rust recipe (toolchain copied from `rust:1-slim-trixie`, `CARGO_HOME` in a `.cargo-home` dependency volume, `prep.command: cargo fetch`), and how to add Rust to the base image later.

Verified:
- Unit tests: image choice (no Dockerfile, current, missing, edited, base rebuilt, unlabeled), tag, `Read` (missing, placeholder, symlink); spec uses the project image for the agent in run/prep/fetch and the base for the volumes service; CLI session image and warnings with Docker stubbed, no image inspection without a Dockerfile, Dockerfile among the probed read-only mounts.
- Integration tests: a project image that installs `tree` via apt has it in a contained sandbox with all probes passing, and counts as current after build and stale after an edit; `COPY .env` fails for lack of context. Full `-tags integration` suite passes.
- CLI smoke run on a throwaway project under `~/Library/Caches`: missing-image warning in `check`, `build`, `run -- tree --version` after 65 probes, stale warning after editing the Dockerfile, no placeholders left.

Not covered: the Rust recipe was not built. `shell` (issue 14) doesn't exist yet; it should build its session through `prepareSession` to get the image. `clean` (issue 16) should remove the project image. No confirmation before `build`, since the agent can't write the Dockerfile.
