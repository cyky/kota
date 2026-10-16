# DONE — 04 — `localllm run` with the Engine VM, `--no-engine`, and the agent told

Status: done

Resolved in: 5b73f91

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

`localllm run` starts and stops the project's **Engine VM** around the **Session**, for opencode and pi, as `shell` does. `--no-engine` on `run` and `shell` starts a **Session** without it, on purpose. The agent's instructions and an environment variable say whether the **Engine VM** is available. When it isn't, the agent must report container **Gates** as not run instead of passed.

## Acceptance criteria

- [x] `run` starts the **Engine VM** for engine projects, with both agents
- [x] `--no-engine` skips the **Engine VM** on `run` and `shell`, and has no effect on projects without one
- [x] The opencode and pi instructions state engine availability correctly in both states
- [x] Unit tests for the agent config and SandboxSpec in both states
- [x] README updated

## Blocked by

- `03-engine-probes-and-refusal.md`

## Comments

**2026-10-16 — Done.** Verified on macOS (Apple silicon, Rancher Desktop):

- `run` of an engine project boots the Engine VM, loads the images and runs the engine probes, with opencode and with pi (new `TestEngineInShell` subtests: `LOCALLLM_ENGINE=available`, `postgres:16-alpine` loaded, the note readable and read-only, its directory writable).
- `--no-engine` on `run` and `shell` starts without the VM, forwarder, certificates or `DOCKER_HOST`, also when the base disk or archives are missing; `LOCALLLM_ENGINE=unavailable` and the note say container tests must be reported as not run. On a project without an `engine:` section, the spec is identical with and without the flag, and no variable or note is added.
- The note reaches the model: opencode 2.0.25 and pi 1.2.0, run offline against a fake model server, both included it in their prompt. opencode accepts the config's `instructions` key but loads nothing from it, so the note is a global `~/.config/opencode/AGENTS.md` (with `~/.config` and `~/.config/opencode` on their own tmpfs, mounted only when there is a note). pi gets `APPEND_SYSTEM.md` in its agent directory.
- Unit tests cover the SandboxSpec and the note in all three states, flag parsing, and the mounted note for both agents. Full unit tests and the CLI integration package pass.

Found on the way, not fixed here (exists on master, reproduced with this work stashed): two sessions started back to back on a prepped Go project sometimes see seeded protected files (`.envrc`, `.git/commondir`, `.localllmignore`, `localllm.Dockerfile`) as writable. The probes catch it and refuse the session. Because of it, `shell --no-engine` is covered by a unit test, not an integration test. Not filed yet.

Also seen: in an offline test in a non-git workspace, opencode didn't load the project's own `AGENTS.md`. Not investigated.

Not verified: a real interactive agent session against a real model, and Linux (issue 09).
