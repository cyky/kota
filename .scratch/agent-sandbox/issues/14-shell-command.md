# DONE — 14 — `shell`: plain shell in the sandbox

Status: done
Resolved in: this commit (`feat: add offline sandbox shell command`)

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`localllm shell [dir]` opens an interactive shell in exactly the same sandbox as `run` (mounts, masks, networks, user, hardening), without starting an agent. Probes run first, same as `run`.

## Acceptance criteria

- [x] `localllm shell` drops into `/workspace` as the host UID
- [x] The same probes run and block the shell on failure
- [x] Network and filesystem restrictions match `run` (unit test: specs differ only in the command)

## Blocked by

- `02-containment-probes-and-check.md`

## Comments

**2026-10-14 — Done.** Added `localllm shell [flags] [dir]`, using `prepareSession` and the same offline startup, configuration, containment checks, cleanup and change report as `run`. Only the container command changes to `bash --noprofile --norc -i`. No agent starts. `--agent` selects the config/state volume; `--model`, `--gateway` and `--ro` are shared with `run`. The gateway must be available for model discovery and the gateway probe. Shell rejects `--auto`, `--unsafe-skip-check` and command overrides, and does not request agent full-auto confirmation. Updated CLI help and README.

Verified:
- `go test ./...` passes. New tests compare the complete run/shell specifications (normalizing session-directory paths) for opencode and pi, including masks, dependency volumes, read-only mounts, gateway key, network and hardening; assert interactive TTY, workspace and host UID/GID; exercise the CLI with a fake Docker executable to prove missing probe results block shell startup and protection placeholders are cleaned up; reject run-only options.
- Real Docker CLI smoke test: all 65 containment probes pass before Bash opens; `pwd` reports `/workspace`; UID 501 and GID 20 match the host; `exit` returns successfully and placeholders are removed.

The configured gateway was unavailable during the smoke test; a temporary host-loopback HTTP stub served the model list instead. No model inference was exercised. The full integration suite was not rerun; this issue shares the existing sandbox and probe implementation.
