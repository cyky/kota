# 01 — Renovate keeps opencode, pi and the pinned images up to date

Status: ready-for-agent
Category: enhancement

## What to build

Everything the sandbox runs is pinned, so builds are reproducible, but each pin is bumped by hand. Add a Renovate config so updates arrive as pull requests:

| Pin | Where | What Renovate needs |
|---|---|---|
| `node:24-trixie-slim@sha256:…`, `golang:1.27-trixie@sha256:…` | `ARG NODE_IMAGE`, `ARG GO_IMAGE` in `assets/images/base/Dockerfile` | Docker digest and tag updates. Check whether the dockerfile manager picks up an image in an `ARG` default that `FROM` uses; if not, a custom regex manager. |
| `alpine:3.22@sha256:…` | `ARG ALPINE_IMAGE` in `assets/images/forwarder/Dockerfile` | As above |
| `OPENCODE_VERSION` (`@opencode/cli`) | base Dockerfile | Custom regex manager, `datasource=npm` |
| `PI_VERSION` (`@earendil-works/pi-coding-agent`) | base Dockerfile | Custom regex manager, `datasource=npm` |
| `CRANE_VERSION` (`github.com/google/go-containerregistry`) | base Dockerfile | Custom regex manager, `datasource=go` or `github-releases` |
| Go modules and the `go` directive | `go.mod` | The built-in gomod manager, with `postUpdateOptions: ["gomodTidy"]` |

- Put a `# renovate: datasource=... depName=...` comment above each `ARG` that a regex manager reads, so the pin and its source stay together in the file.
- Keep the tag next to the digest on image pins, as today, so a reader still sees what the image is.
- **No automerge.** These pins decide what runs in every sandbox, and opencode's install runs its own scripts (`--allow-scripts=@opencode/cli`). Each update is a PR you review.
- Group by risk: opencode and pi each get their own PR, since a new version can change their config schema or the keys the agent overrides allow (`docs/agent-setup.md`). Base image digests can be grouped together.
- A schedule (for example weekly) and a dependency dashboard issue, so updates don't arrive one by one.

Out of scope: the Debian cloud image for the Engine base disk (`cloudImages` in `internal/engine/base.go`). It is a dated URL plus a SHA512 from `SHA512SUMS`, with no Renovate datasource. Note it in the config's README section, or file a separate issue if it should be automated.

There is no CI, so a Renovate PR isn't tested automatically. The PR body (Renovate's `prBodyNotes`) should say how to check one: `kota build`, `go test ./...`, `go test -tags integration ./...`, then a short `kota run` session with each agent.

## Acceptance criteria

- [ ] `renovate.json` at the repo root, valid by `npx --package renovate renovate-config-validator`
- [ ] A local dry run (`renovate --platform=local --dry-run`, or `LOG_LEVEL=debug`) finds every pin in the table above
- [ ] opencode and pi updates come as separate PRs; nothing automerges
- [ ] `docs/development.md` says how pins are updated, how to check a Renovate PR, and that the Debian cloud image is still bumped by hand

## After merging (human)

- Install the Renovate GitHub app on `cyky/kota`, and merge or close its onboarding PR.

## Blocked by

None - can start immediately
