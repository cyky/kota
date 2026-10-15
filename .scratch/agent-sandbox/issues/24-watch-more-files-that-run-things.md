# 24 — Report more files that run things

Status: ready-for-agent

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

- [ ] Each file above is reported when added, modified or deleted
- [ ] Unit tests cover the new patterns, including a nested `build.rs` and a file inside `.idea/`
- [ ] README's list of reported files updated

## Blocked by

None - can start immediately
