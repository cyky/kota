# 12 — `prep`: install dependencies with network, no agent

Status: ready-for-agent

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

- [ ] On a pnpm TS project: `prep` then `run` → agent can run `pnpm test` offline
- [ ] On a Go project: `prep` then `run` → agent can run `go test ./...` offline
- [ ] The host's `node_modules` (if any) is untouched and not visible inside the sandbox
- [ ] A second project reuses the Go module cache / pnpm store
- [ ] During `prep`, `.env` is masked and `.git/hooks` is read-only
- [ ] Unit tests on SandboxSpec for prep vs run: network, key, volume mounts
- [ ] Unit tests for package manager detection

## Blocked by

- `04-host-execution-protection.md`
- `05-secret-masking.md`
- `11-gateway-api-key.md`
