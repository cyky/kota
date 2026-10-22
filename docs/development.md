# Development

Working on kota itself. How it is put together is in [Architecture](architecture.md); its terms are in [`CONTEXT.md`](../CONTEXT.md), and decisions in [adr/](adr/).

## Layout

- `cmd/kota`: the command line, and the end-to-end tests of each command
- `internal/`: one package per part (the sandbox spec, masks, probes, prep, the change report, the Engine VM, ...); [Architecture](architecture.md#packages) shows how they depend on each other
- `assets/`: what is built into the binary: the image Dockerfiles, the agents' bundled config and instructions, and `models.yaml`

## Build

`go install ./cmd/kota`, then `kota build` when anything under `assets/images` changed.

## Tests

- Unit tests: `go test ./...`. They need git, but not Docker.
- Integration tests need Docker: `go test -tags integration ./...`. The prep and project image tests also need network access. The Engine VM tests also need QEMU and the Engine base disk (`kota build`), and are skipped without them.

## Pinned versions

Everything the sandbox images run is pinned, so builds are reproducible: the base images by tag and digest, opencode, pi and crane by version (the `ARG`s in `assets/images/*/Dockerfile`), and the Go modules and `go` directive in `go.mod`. [Renovate](https://docs.renovatebot.com/) keeps them up to date with pull requests, configured in [`renovate.json`](../renovate.json):

- Updates come on Monday mornings, and the dependency dashboard issue lists everything that is waiting.
- A release must be at least 7 days old before Renovate proposes it. A fix for a known vulnerability (from GitHub's alerts or OSV) comes at once.
- opencode and pi each get their own PR, since a new version can change their config schema or the keys that [agent overrides](agent-setup.md) allow. Base image digests come in one PR.
- Nothing automerges: these pins decide what runs in every sandbox.

A version `ARG` that Renovate should track has a `# renovate: datasource=... depName=...` comment above it. Images in an `ARG` that `FROM` uses need no comment.

### Checking a Renovate PR

There is no CI, so check each one by hand on its branch:

1. `go install ./cmd/kota`, then `kota build`
2. `go test ./...` and `go test -tags integration ./...`
3. A short `kota run` session with each agent

### Bumped by hand

The Debian cloud image for the Engine base disk (`cloudImages` in `internal/engine/base.go`) is a dated URL and a SHA-512 with no Renovate datasource. To bump it, take a newer dated release and its `SHA512SUMS` from cloud.debian.org.

## Issues and design notes

PRDs and issues are markdown files in [`.scratch/`](../.scratch/README.md), one directory per feature, with the reasoning kept in each issue's comments.
