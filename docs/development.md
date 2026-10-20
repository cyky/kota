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

## Issues and design notes

PRDs and issues are markdown files in [`.scratch/`](../.scratch/README.md), one directory per feature, with the reasoning kept in each issue's comments.
