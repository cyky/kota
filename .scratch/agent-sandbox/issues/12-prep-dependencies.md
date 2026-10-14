# DONE — 12 — `prep`: install dependencies with network, no agent

Status: done
Resolved in: `a8dc094`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`localllm prep [dir]` installs the project's dependencies in the same image, with network access and without an agent, so the offline `run` has what it needs.

- Detect the package manager: npm, pnpm, yarn (via corepack, from lockfiles / `packageManager` field) and Go modules. Allow an explicit override command in per-project config.
- Dependency directories (`node_modules`, `.venv`, `target/`, …) live in per-project Docker volumes mounted over those paths in both `prep` and `run`, so Linux binaries never land in the host's project directory.
- Shared volumes for the Go module cache and the pnpm store, reused across projects.
- `prep` applies the same masks and read-only `.git/hooks`, `.git/config` and `.envrc` mounts as `run`, and receives no gateway key or forwarder.
- `run` shows a hint when the dependency volume is empty but a manifest exists.

## Acceptance criteria

- [x] On a pnpm TS project: `prep` then `run` → agent can run `pnpm test` offline
- [x] On a Go project: `prep` then `run` → agent can run `go test ./...` offline
- [x] The host's `node_modules` (if any) is untouched and not visible inside the sandbox
- [x] A second project reuses the Go module cache / pnpm store
- [x] During `prep`, `.env` is masked and `.git/hooks` is read-only
- [x] Unit tests on SandboxSpec for prep vs run: network, key, volume mounts
- [x] Unit tests for package manager detection

## Blocked by

- `04-host-execution-protection.md`
- `05-secret-masking.md`
- `11-gateway-api-key.md`

## Comments

**2026-10-14 — Done.** Implemented in `a8dc094` on `master`.

- `internal/deps.Detect`: npm, pnpm and yarn from the `packageManager` field or lockfile (frozen installs with a lockfile; pnpm uses the shared store via `--store-dir`), Go modules (`go mod download` per module, once with `go.work`). `localllm.yaml` `prep.command` replaces the steps, `prep.dirs` adds dependency directories (relative, not in `.git`, no symlinks). `Plan.Conflict` refuses a volume that is, contains or sits inside a protected or masked path.
- `localllm.yaml` is protected by the mask layout (read-only, placeholder when missing), since its command runs with network.
- Spec: `ModePrep` puts the agent on the normal network only, with no forwarder, internal network or key. `Inputs.Deps` are per-project volumes (`DepVolume`, prefix `DepVolumePrefix` for issue 16), writable in both modes. Shared external volumes: Go module cache and corepack cache (read-only in run), pnpm store (prep only). Run sets `GOPROXY=off` and `COREPACK_ENABLE_NETWORK=0`. A `volumes` service (root, only `CAP_CHOWN`, `network_mode: none`, no project mount) chowns new dependency volumes and lists empty ones; `docker.Start` runs it once and creates the external volumes.
- CLI: `prep [dir]` builds the session with `ModePrep` (no gateway or key loaded), runs the install script attached, and reports changed files that run things. `run`/`check` print a hint for empty dependency volumes. Mount points Docker creates on the host for the volumes are removed when empty. `check` shows volume names in the mounts.

Verified:
- Unit tests: package manager detection (field vs lockfile, yarn classic vs berry, workspaces for pnpm/npm/yarn, nested Go modules, `go.work`, config override and errors, symlinked dirs, conflicts); spec prep vs run (networks, forwarder, key, env, dependency volumes, shared caches and their access per mode, the volumes service, volume names); CLI prep session (no key, masks and git protections read-only, dep volume, install command, mount point cleanup, host `node_modules` kept, volume over a protected path refused).
- Integration tests (Docker and network, test-only cache volumes): pnpm project prep then `pnpm test` offline, host `node_modules` untouched and invisible, hint before prep only; Go project prep then `go test ./...` offline, module cache not writable in run; a second project preps with no network at all from the shared pnpm store/corepack cache and Go module cache, while a project with an empty cache fails the same way; during prep `.env` reads empty, writes to hooks, git config, `.envrc` and `localllm.yaml` fail, no key or gateway host, registry reachable. Full `-tags integration` suite passes.
- CLI smoke run of `localllm prep` on a throwaway npm + Go project under `~/Library/Caches`: install ran, the `node_modules` mount point and placeholders were removed from the host.

Not covered: no containment probes in prep (it has a normal network by design, which also reaches host loopback services and the LAN). Python/Rust dependency dirs are only via `prep.dirs`. A husky `prepare` script can't set `core.hooksPath` during prep. `.npmrc` stays masked unless unmasked. No real-project trial yet (issue 17).

**2026-10-14 — Follow-up: security fix in `652b1fa`.** Background reviews flagged two problems with `a8dc094`: install scripts ran with the shared caches writable (cross-project poisoning), and an agent session could write manifests, scripts or `node_modules` that the next prep ran with network access.

- Two-phase prep: `ModeFetch` runs only package manager commands (`corepack install`, `pnpm fetch --ignore-pnpmfile`, `go mod download`) with the caches writable and fresh dependency volumes; `ModePrep` runs the installs (`pnpm install --offline`, `yarn install`, `npm ci`, the `localllm.yaml` command) with all shared caches read-only. pnpm without a lockfile installs into a private store. `packageManager` must be a plain version (no URLs). `COREPACK_ENABLE_AUTO_PIN=0` everywhere.
- Dependency volumes are deleted and recreated before each prep.
- `internal/prepgate` records the project after each successful prep under the user cache dir. The next prep asks for `y` on the first run, when prep inputs changed (manifests, lockfiles, pnpm/yarn config and releases, patches, Go module files, `localllm.yaml`), or when the install runs project code (lifecycle scripts, pnpmfile, `yarnPath`, the config command) and any other file changed. No terminal aborts.

Verified: unit tests for cache access per mode, fetch/install commands, `packageManager` refusals, hooks, gate decisions and prompt output; integration tests that the install phase can't write the caches and the fetch phase can, that a file planted in `node_modules` by a run session is gone after prep, plus the earlier pnpm/Go/offline-reuse tests (pnpm now installs offline from the read-only store); CLI smoke run of the prompt (first run, decline, accept, unchanged rerun, changed `setup.js`). Full `-tags integration` suite passes.

Not covered: changes in dependency and build directories (`dist`, `build`, `vendor`, ...) don't count for the confirmation; the confirmation relies on the user's review; registry packages can still run their own install scripts during their project's prep. `go.sum` must be complete before prep (fetch doesn't run `go mod tidy`). `clean` (issue 16) should also remove the prep record.
