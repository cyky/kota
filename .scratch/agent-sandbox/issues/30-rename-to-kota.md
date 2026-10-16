# 30 — Rename the project to `kota`

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Rename localllm to **kota**, Finnish (and Sámi) for a small hut: a simple shelter where the agent works, apart from everything else. The command becomes `kota run`, `kota prep`, `kota check`, and the project files `kota.yaml` and `.kotaignore`.

Decided on 2026-10-16:

- **Name:** `kota`. Checked: no `kota` command or Homebrew formula exists, and no CLI tool by that name came up. Near neighbours, not conflicts: Microsoft's `kiota` (OpenAPI client generator) and Kata Containers.
- **Fresh start, no migration.** kota doesn't read localllm's volumes, data dir, project files or environment variables. The README tells users to run `localllm clean` in each project before switching, and lists what to remove afterwards.

## What changes

Every place the name appears, with no `localllm` left outside history (old `.scratch` issues and git log):

- **Go module and binary:** `module localllm` becomes `module kota`; `cmd/localllm` becomes `cmd/kota`; all import paths follow.
- **Images:** `localllm-base`, `localllm-forwarder`, `localllm-engine-probe`, project images (`localllm-project-*`), and the labels `projectimage` writes.
- **Docker resources:** volumes `localllm-state-*`, `localllm-deps-*`, `localllm-cache-*` (gomod, corepack, pnpm-store); compose project names (`sandbox.ProjectName`); any container or network names derived from them.
- **Host data:** `localllm/` under the user cache dir becomes `kota/` (`hostdata.Default`). That covers the registry, prep records, sessions and engine data, including the Engine base disk, which `kota build` builds again.
- **Engine VM:** the VM name prefix (`localllm-engine-*`), the `fw_cfg` names (`opt/localllm/...`), and anything else in the base disk definition. The definition changes, so existing base disks are reported as outdated; with a fresh start that doesn't matter.
- **In-sandbox paths:** `/etc/localllm/...` (agent config, engine certificates) becomes `/etc/kota/...`.
- **Environment variables:** `LOCALLLM_ALLOWED_ROOT`, `LOCALLLM_GATEWAY`, `LOCALLLM_GATEWAY_KEY`, `LOCALLLM_GATEWAY_KEY_FILE`, `LOCALLLM_ENGINE` (seen by the agent) and the test-only `LOCALLLM_PIN`, `LOCALLLM_PROBE_PUBLISHED`, `LOCALLLM_REGISTRY_HELPER`, `LOCALLLM_TEST_ARGS`, `LOCALLLM_TEST_MAIN` become `KOTA_*`.
- **Project files:** `localllm.yaml` becomes `kota.yaml`, `.localllmignore` becomes `.kotaignore`, `localllm.Dockerfile` becomes `kota.Dockerfile`. The protections (read-only mounts, the prep gate's input list, the change report's watched files) follow the new names.
- **Agent-facing text:** the engine note, opencode/pi instructions, and probe and error messages that say "localllm".
- **Docs:** README (title, usage, every command), `CONTEXT.md`, the ADR, `docs/architecture.md`, `docs/agents/*`. Open `.scratch` issues (26–29) are updated; closed issues and the PRDs keep the old name as history.

## Acceptance criteria

- [ ] `git grep -i localllm` matches only closed `.scratch` issues, the PRDs and the README's migration note
- [ ] `go build ./cmd/kota` builds a `kota` binary; `go test ./...` and `go test -tags integration ./...` pass
- [ ] A project with only `localllm.yaml`, `.localllmignore` or `localllm.Dockerfile` is treated as having none of them. The README says to rename them.
- [ ] After a fresh `kota build` and `kota prep`, `kota run` and `kota shell` work, including the Engine VM, with `kota` volumes, images and data only
- [ ] The README has a "Migrating from localllm" section: run `localllm clean` (and `--shared-caches`) per project before switching; rename the three project files; then remove the old images (`docker rmi localllm-base localllm-forwarder` and project images), the old data dir (`localllm/` in the user cache dir, including the Engine base disk) and the `localllm` binary
- [ ] The README says what the name means

## Out of scope

- Renaming the repository folder or remote. Note: Claude's memory for this project is keyed by the folder path, so renaming the folder starts with empty memory unless it is moved along.
- Any compatibility with the old names.

## Blocked by

None - can start immediately. Do it before 26–29 land, or update them as part of this.
