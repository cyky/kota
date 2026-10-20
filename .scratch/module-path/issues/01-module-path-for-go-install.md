# 01 — Module path github.com/cyky/kota, so go install works

Status: ready-for-agent
Category: enhancement

## What to build

The module is called `kota` in `go.mod`. With the repo public, the natural install is `go install github.com/cyky/kota/cmd/kota@latest`, but Go refuses it: the module path doesn't match where it was fetched from. Today the README has users clone the repo and run `go install ./cmd/kota` instead.

Change the module path to `github.com/cyky/kota`:

- `go.mod`: `module github.com/cyky/kota`.
- Every import of `kota/...` becomes `github.com/cyky/kota/...` (61 Go files, in `cmd/kota` and `internal/`).
- README quick start: install with `go install github.com/cyky/kota/cmd/kota@latest`. Keep the clone and `go install ./cmd/kota` route for changing `assets/models.yaml`, which is built into the binary (the README's Models section says to reinstall after editing it).
- `docs/development.md` and any other doc that shows the install command.

The binary name stays `kota`, and nothing kota writes (image names, volume names, the data dir) depends on the module path, so existing installs keep working.

## Acceptance criteria

- [ ] `go.mod` declares `github.com/cyky/kota`, and no Go file imports `kota/...`
- [ ] `go build ./...`, `go vet ./...` and `go test ./...` pass
- [ ] `go test -tags integration ./...` builds (running it needs Docker)
- [ ] The README installs with `go install github.com/cyky/kota/cmd/kota@latest`, and still explains the clone route for a changed `models.yaml`
- [ ] After the change is pushed, `go install github.com/cyky/kota/cmd/kota@latest` in a clean `GOPATH` installs a working `kota` (`kota help` runs)

## Blocked by

None - can start immediately
