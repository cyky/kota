# DONE — 15 — SessionRegistry: one session per project, model-clash warning

Status: done

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **SessionRegistry** module (`Acquire(project, agent, model) → lock | busy`, `Active() → sessions`) used by `run`, `shell` and `prep`.

- Only one session per project at a time; a second attempt is refused and names the running session.
- Different projects can run at the same time.
- Stale locks from crashed sessions are detected (dead PID / container gone) and cleaned up.
- When a new session uses a different model than a running session, warn that llama-swap will swap models back and forth.

## Acceptance criteria

- [x] Two `run`s on the same project → second refused
- [x] `run`s on two different projects → both start
- [x] After killing a session abruptly, the next `run` on that project starts
- [x] Different-model sessions → warning shown
- [x] Unit tests for acquire/busy/stale/clash

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Added `internal/sessionregistry` with `Acquire(project, agent, model)`, `Active()`, per-project leases and atomic model publication/clash detection. Metadata includes the resolved project, operation/agent, model, host PID and start time. Registry records live in the host user cache outside the sandbox. Persistent per-project OS file locks prevent competing owners and release automatically on process death, avoiding PID-reuse errors; a short registry guard serializes metadata access. Lock files stay in place after clean release to avoid inode replacement races.

`run`, `shell` and `prep` acquire before session preparation and hold the lease through container teardown, placeholder cleanup, reporting and prep-state recording. `check` also acquires: it shares the Compose project and protection placeholders and must not tear down a running session. Busy errors name the current operation, agent, PID and start time. Symlink aliases resolve to the same project lock. Errors and declined confirmations release the lease.

A free lock with a stale record triggers recovery before reuse. `docker.RecoverProject` removes only containers labeled with that Compose project, including one-off/stopped containers; it preserves named volumes and other projects. Failed Docker recovery refuses startup and retains the old record for retry. Partial records left by a crash can be recovered too. Old session directories and mount placeholders left by a killed process are not removed by registry recovery.

After gateway discovery, `run` publishes the resolved model and warns for other active agent sessions using different models. Publication and comparison are atomic, so concurrent starts cannot both miss the warning. Shell, prep and check do not publish a model. Models changed from inside an already-running agent are not tracked.

Verified:
- `go test ./...` passes. Registry tests cover acquire/busy/release, active sessions, independent projects, same/different/empty models, simultaneous acquisition/publication, closed leases, partial metadata, and a real helper process killed while holding a lease. Recovery errors retain the stale record; retry succeeds.
- CLI tests prove run/shell/prep/check refuse an occupied project before creating protection placeholders, symlink aliases cannot bypass the lock, another project is accepted, startup errors release leases, and a resolved `--model` produces a warning naming the conflicting session.
- Docker adapter tests cover already-gone containers, exact project-label scoping, orphan removal and Docker listing/removal failures.
- `go test -race ./internal/sessionregistry ./internal/docker ./cmd/localllm` passes.
- Real Docker smoke test with two temporary projects and a host-loopback stub gateway: both runs pass containment probes and run concurrently; a second run/shell/prep/check for the occupied project is refused; a different starting model warns; killing the first host CLI leaves orphaned containers, and its replacement recovers them and starts while the second project's session remains running. Both normal exits remove their containers. Temporary project directories and project-specific state volumes were removed afterwards.

No model inference was exercised. The full integration suite was not rerun; the Docker smoke test exercises the shared startup/probe path and the new registry/recovery behavior.

**2026-10-14 — Review fixes.** Removed the temporary `Resolved in` line. Validated project and read-only mount paths now travel with the lease into preparation, so retargeting CLI symlink arguments after locking cannot switch the sandbox to another project. Model publication/scan failures warn and continue; scans skip entries that vanish before opening. Recovery releases the global metadata guard while retaining the per-project flock, then reacquires the guard to publish the replacement record. Failed recovery still retains the stale record for retry.

Verified the regressions before fixing them: retargeted project/reference symlinks changed the prepared mounts; an unrelated unopenable registry entry prevented the command from starting; paused recovery blocked unrelated registry operations; a vanished entry aborted the active-session scan. All four regression tests now pass, along with `go test ./...` and `go test -race ./internal/sessionregistry ./cmd/localllm`. The paused-recovery test checks Busy reporting, unrelated acquisition, model publication, active-session reads and clean release while recovery remains blocked. The Docker smoke test above was not repeated for these review fixes.
