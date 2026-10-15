# DONE — 18 — Run the full-auto dirty-tree check in an offline container

Status: done

Resolved in: b500603

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Before a full-auto session (`run --auto`, or `run --agent pi`), localllm runs `git status` on the host to find uncommitted changes. Host git runs whatever the repository's config tells it to, and turning off `core.fsmonitor` is not enough: `git status` also runs clean filters. This was confirmed in review: a repo with `filter.x.clean` set and `* filter=x` in `.gitattributes` ran the filter command under the exact command line localllm uses.

The agent can set this up. In a non-git project it can `git init`, add a filter to the new `.git/config`, write `.gitattributes` and change a file's timestamp. The next full-auto `run` then executes that command on the Mac, triggered by localllm itself rather than by the user's own git use (which the README already lists as a limit).

Run the check in a throwaway container instead: the base image, the project mounted read-only, `--network none`, the same hardening as the sandbox (host UID/GID, all capabilities dropped, `no-new-privileges`). The check needs no mask layout, so it can run before anything is written to the host. Whatever a filter does, it then runs offline, unprivileged and without write access to the project.

The prompt, the listed changes and the abort message stay the same. Non-git projects are still not asked.

## Acceptance criteria

- [x] No host `git` command runs against the project's repository as part of the full-auto confirmation
- [x] Integration test: a repo whose config sets a clean filter that writes a marker file; the confirmation runs, and no marker appears on the host
- [x] Uncommitted changes inside the project (modified, staged, untracked) are still listed and require typing `y`; changes outside a subdirectory project are still ignored
- [x] Non-git projects, and worktrees or submodule checkouts whose git dir is outside the project, behave as documented (no prompt, or a clear message)
- [x] The check container gets no network and cannot write the project
- [x] README updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.** `approval.FindRepo` finds the repository without git: `.git` in the project or a directory above, following the `gitdir:` and `commondir` pointers of a `.git` file; a symlinked `.git` is refused. `git status` runs through `docker.RunOffline`: base image, `--network none`, read-only root, all capabilities dropped, `no-new-privileges`, host UID, `--pull never`, and read-only mounts of only the project, its `.git`, and the git dir and common dir, each at its host path. Paths outside the project must pass `pathpolicy.CheckReadOnly` (the `--ro` rules).

Verified: an integration test with a clean filter and a changed file timestamp. In the container the filter wrote nothing on the host and the status was correct; a control run through host git did create the marker. A linked-worktree status works in the container, with a comma in the path to check `--mount` quoting. Unit tests cover discovery (repo root, subdirectory, worktree, worktree subdirectory, no repo, symlinked `.git`), the policy check and the mount quoting.

Not verified: the CLI wrapper that applies the policy before calling Docker has no end-to-end test (no CLI end-to-end tests, per the PRD). Behaviour change: bare-repository files at the project root no longer count as a repository, so such a project isn't asked about.
