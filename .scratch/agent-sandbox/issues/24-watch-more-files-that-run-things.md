# DONE — 24 — Report more files that run things

Status: done

Resolved in: 1526816

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

After a session, the change report lists the watched files that changed. Several common files that run code on the host are not in the list:

- `mise.toml`, `.mise.toml`, `.tool-versions`: mise runs hooks and sets env on `cd`, like direnv
- `.gitattributes`, `.gitmodules`: select filter and diff drivers, and submodule URLs
- `.cargo/config.toml`, `build.rs`: run on the host by cargo (the README suggests Rust)
- `justfile`, `Justfile`, `Taskfile.yml`, `Taskfile.yaml`
- `.idea/`: run configurations and tool settings

Add them to the watched list, at any depth where the tool reads them there. Keep the README list in step.

## Acceptance criteria

- [x] Each file above is reported when added, modified or deleted
- [x] Unit tests cover the new patterns, including a nested `build.rs` and a file inside `.idea/`
- [x] README's list of reported files updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.** `changereport.Watched` gains `mise.toml`, `.mise.toml`, `mise.local.toml`, `.mise.local.toml`, `.tool-versions`, `.gitattributes`, `.gitmodules`, `.cargo/`, `build.rs`, `justfile`, `Justfile`, `.justfile`, `Taskfile.yml`, `Taskfile.yaml` and `.idea/`, all matched at any depth (cargo reads `.cargo/config.toml` in every directory up from where it runs). The `mise.local.toml` variants and `.justfile` go beyond the issue's list. As for every watched file, those under skipped directories (`target/`, `node_modules/`, ...) aren't reported. README's list of reported files updated.

Verified: a new `TestDiff` case adds every new file at the root and nested (`crates/core/build.rs`, `.idea/runConfigurations/x.xml`, ...) and checks that `target/debug/build.rs` and `src/build.rs.md` aren't reported. `TestHostToolFilesModifiedAndDeleted` covers modification and deletion of a nested `build.rs`, an `.idea/` file, `.cargo/config.toml` and `Justfile`. `go vet` and the unit suite pass; the integration suite wasn't run, since only the pattern list changed.
