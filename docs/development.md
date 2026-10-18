# Development

Working on kota itself. How it is put together is in [Architecture](architecture.md); its terms are in `CONTEXT.md`, and decisions in [adr/](adr/).

## Tests

Integration tests need Docker: `go test -tags integration ./...`. The prep and project image tests also need network access. The Engine VM tests also need QEMU and the Engine base disk (`kota build`), and are skipped without them.
