# Design notes and issues

kota's issue tracker: every feature's PRD, its issues and the comments made while building them, done or open. They are kept in the repo, next to the code, so the reasons behind a decision stay findable. How the files are laid out is in [docs/agents/issue-tracker.md](../docs/agents/issue-tracker.md); the terms are as in [CONTEXT.md](../CONTEXT.md).

kota was called `localllm` until [issue 30](agent-sandbox/issues/30-rename-to-kota.md). Older PRDs and issues use that name, as they were written.

## Features

| Directory | What it covers |
|---|---|
| [agent-sandbox](agent-sandbox/PRD.md) | The original PRD: the offline sandbox, masks, protections, prep, the change report, both agents. Also holds the [trial run report](agent-sandbox/trial-run-report.md). |
| [engine-vm](engine-vm/PRD.md) | A QEMU VM per session for tests that start containers ([ADR 0001](../docs/adr/0001-engine-vm-for-agent-containers.md)) |
| [agent-module](agent-module/PRD.md) | Refactor: one module per agent |
| [change-report](change-report/issues/) | Refactor: the change report and the `--no-history` return as modules |
| [offline-git](offline-git/issues/) | git in throwaway offline containers, never on the host |
| [probe-from-mounts](probe-from-mounts/issues/) | Containment probes derived from the sandbox's mounts |
| [session-verify](session-verify/issues/) | One verify step for `run`, `shell` and `check` |
| [stale-mounts](stale-mounts/issues/) | Mounts that vanish when a file is replaced on the host |
| [prep-npmrc](prep-npmrc/issues/) | `prep` reads the project-root `.npmrc` |
| [module-path](module-path/issues/) | The Go module path, so `go install` works from GitHub |
| [renovate](renovate/issues/) | Renovate keeps the pinned agents and images up to date |

## Open issues

- [Any OpenAI-compatible gateway, such as LiteLLM](agent-sandbox/issues/34-any-openai-compatible-gateway.md) (needs triage)
- [Engine VM on Linux](engine-vm/issues/09-engine-vm-on-linux.md) (ready for a human)
- [On Linux, replacing a protected file on the host drops its mount](stale-mounts/issues/02-host-replace-on-linux.md) (needs triage)
- [Module path github.com/cyky/kota, so go install works](module-path/issues/01-module-path-for-go-install.md) (ready for an agent)
- [Renovate keeps opencode, pi and the pinned images up to date](renovate/issues/01-renovate-for-pinned-versions.md) (ready for an agent)

An issue's title starts with `DONE — ` once it is closed, and its `Resolved in:` line names the commits.
