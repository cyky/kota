# 23 — Protect git config includes inside the project

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`.git/config` is read-only in the sandbox, but the config can point at files in the project that stay writable:

- `include.path` and `includeIf.*.path` targets inside the project (e.g. a committed `.gitconfig` that projects include). The agent can write `core.fsmonitor`, `core.hooksPath` or an alias into the included file, and host git runs it.
- Values that run a project file: `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `alias.* = !...`, `filter.*`, `diff.*.textconv`, `diff.*.command`, `merge.*.driver`, `credential.helper`. If they name a script in the project, the agent can change the script.

`core.hooksPath` is already handled the same way: a target inside the project is mounted read-only.

Handle includes like `core.hooksPath`, at every config level and in submodule git dirs too: an include target inside the project is mounted read-only, or held by a placeholder when it is missing. Refuse it when the path goes through a symlink. Show a warning, not a block, for the executable values above when they reference a path inside the project, naming the key and the file. Report changes to those files in the change report.

## Acceptance criteria

- [ ] An `include.path` target inside the project is read-only in the sandbox, and a probe verifies it
- [ ] A missing include target can't be created by the agent
- [ ] An `includeIf` target inside the project is handled the same way
- [ ] A config value that runs a project script prints a warning naming the key and the file, and a later change to that file is reported after the session
- [ ] Includes pointing outside the project are left alone
- [ ] Unit tests with real git repos
- [ ] README's host-execution section updated

## Blocked by

None - can start immediately
