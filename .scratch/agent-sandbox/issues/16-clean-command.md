# DONE — 16 — `clean`: remove a project's volumes

Status: done

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`localllm clean [dir]` removes that project's agent state volumes and dependency volumes after confirmation. Shared caches (Go module cache, pnpm store) are left alone unless an explicit flag is passed.

## Acceptance criteria

- [x] Lists the volumes it will remove and asks for confirmation
- [x] Afterwards, `run` starts with fresh state and `prep` is needed again
- [x] Other projects' volumes are untouched
- [x] Refuses while a session for that project is running

## Blocked by

- `07-agent-config-and-state.md`
- `12-prep-dependencies.md`

## Comments

**2026-10-14 — Done.** Added `localllm clean [flags] [dir]` (default: current directory). It validates paths and acquires the project's session lease before listing, confirmation and removal. It does not prepare a sandbox or parse dependency configuration, so cleanup works with removed/broken manifests and without a gateway. Busy projects are refused before Docker is called.

`docker.ListVolumes` lists actual existing names; `sandbox.ProjectVolumes` selects state volumes for any current/retired agent and dependency volumes whose exact project hash matches, including directories no longer in the manifest. The sorted list is printed before asking for `y`/`yes`; declining, blank input or EOF leaves everything in place. Successful cleanup also removes the host prep confirmation record. Empty projects report nothing to clean. Project images, source files and other projects' volumes are preserved. Docker removal failures are reported and retain the prep record; Docker may have removed some listed volumes before returning an error.

Shared caches are preserved by default. `--shared-caches` explicitly includes Go modules, corepack releases and the pnpm store. SessionRegistry now holds a shared cache reservation for each normal project lease and an exclusive reservation for shared-cache cleanup. The exclusive lease refuses while any session is starting, running or finishing (including confirmations and gaps between prep containers), and blocks new sessions until cleanup completes. Cache reservations release on normal exit, process death and acquisition failure. The existing metadata guard remains short-lived. Updated CLI help and README.

Verified:
- `go test ./...` passes. Volume selection tests cover both agents, retired agents/dependency directories, exact project hashes, malformed/similarly named unrelated volumes, duplicates, shared-cache exclusion and empty inventory.
- CLI tests cover confirmation/decline/blank/EOF, shared-cache inclusion, Docker listing/removal errors, preserving prep records on failure, resetting records on success, empty/record-only cleanup, current-directory default, flags on either side of the directory, invalid flags, broken configuration/missing key file, refusal before Docker on busy projects, and ordinary cleanup while another project is active.
- Registry tests cover shared reservations across projects, refusal of exclusive cleanup until all sessions finish, refusal of new sessions during reset, and releasing reservations after acquisition/recovery failures. Existing crash/concurrency tests still pass.
- `go test -race ./internal/sessionregistry ./internal/sandbox ./cmd/localllm` passes.
- Real Docker smoke test with temporary projects and a host-loopback stub gateway: confirmed busy-project refusal and shared-reset refusal while a session runs; declining preserves volumes; confirming removes opencode/pi state and current/retired dependency volumes while preserving another project's state and all shared caches; the next contained run has fresh state and empty dependencies; the next prep asks for first-prep confirmation and restores dependencies for an offline run. Temporary project state/dependency volumes and prep records were removed afterwards. Actual shared-cache deletion was tested with a fake Docker executable to preserve the user's caches.

Follow-up for the real-project trial: rapid session reuse with transient host mountpoints failed during the first Docker smoke attempt (missing read-only placeholder paths after prep). A comparison using the previous pre-clean CLI also failed on its second prep/run cycle because `.deps` was missing inside the container, without invoking clean. The successful cleanup smoke test used fixture mountpoint directories and empty protection files that remain present between sessions. The underlying session-reuse/mount-visibility cause is not diagnosed here. No model inference or external package downloads were exercised; the full integration suite was not rerun.
