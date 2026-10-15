# DONE — 23 — Protect git config includes inside the project

Status: done

Resolved in: ea60445

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

`.git/config` is read-only in the sandbox, but the config can point at files in the project that stay writable:

- `include.path` and `includeIf.*.path` targets inside the project (e.g. a committed `.gitconfig` that projects include). The agent can write `core.fsmonitor`, `core.hooksPath` or an alias into the included file, and host git runs it.
- Values that run a project file: `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `alias.* = !...`, `filter.*`, `diff.*.textconv`, `diff.*.command`, `merge.*.driver`, `credential.helper`. If they name a script in the project, the agent can change the script.

`core.hooksPath` is already handled the same way: a target inside the project is mounted read-only.

Handle includes like `core.hooksPath`, at every config level and in submodule git dirs too: an include target inside the project is mounted read-only, or held by a placeholder when it is missing. Refuse it when the path goes through a symlink. Show a warning, not a block, for the executable values above when they reference a path inside the project, naming the key and the file. Report changes to those files in the change report.

## Acceptance criteria

- [x] An `include.path` target inside the project is read-only in the sandbox, and a probe verifies it
- [x] A missing include target can't be created by the agent
- [x] An `includeIf` target inside the project is handled the same way
- [x] A config value that runs a project script prints a warning naming the key and the file, and a later change to that file is reported after the session
- [x] Includes pointing outside the project are left alone
- [x] Unit tests with real git repos
- [x] README's host-execution section updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.** New `internal/mask/gitconfig.go`. It starts from the files git reads for the project (`git config --list --show-origin`: system, global, local, worktree and matched includes), every git dir's `config` and `config.worktree` (submodules included), and linked worktrees' `config.worktree`. From each it follows `include.path` and `includeIf.*.path` recursively, resolving relative paths against the including file, whatever the condition. Targets inside the project are protected like `core.hooksPath`: read-only, a placeholder when missing, and refused through a symlink, with the include key in the error. Outside targets aren't mounted but are still read for further includes.

Values that run a command: the keys listed in the issue plus `core.askPass`, `sequence.editor`, `diff.external` and `gpg.program`. When the command word, or an existing argument, resolves to a project file that no read-only mount holds, a warning names the key, the config file and the script. The file goes into the new `Layout.Watch`, and `changereport.SnapshotWith` records it for the change report of `run`, `shell` and `prep`, even inside skipped directories. Words with `$`, `%` or URLs are skipped; a script reached indirectly isn't seen (README limit). README host-execution section updated.

Verified: `TestConfigIncludesProtected` (include.path, missing target, unmatched includeIf, nested include, global includeIf through an outside file, submodule git dir), `TestConfigIncludeOutsideLeftAlone`, `TestConfigIncludeSymlinkRefused`, `TestConfigExecRefs` and `TestSnapshotWith`. They fail with `configs()` disabled. The integration `TestGitProtection` now has an included `.gitconfig` and a missing `includeIf` target: the probes report `read-only /workspace/.gitconfig` and `/workspace/ci`, and editing, renaming or creating them from the container fails. `go vet`, the unit suite and the full integration suite pass. `mask.Plan` on this repo with the real global git config completes without errors.
