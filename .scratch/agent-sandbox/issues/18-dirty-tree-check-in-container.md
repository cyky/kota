# 18 — Run the full-auto dirty-tree check in an offline container

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Before a full-auto session (`run --auto`, or `run --agent pi`), localllm runs `git status` on the host to find uncommitted changes. Host git runs whatever the repository's config tells it to, and turning off `core.fsmonitor` is not enough: `git status` also runs clean filters. This was confirmed in review: a repo with `filter.x.clean` set and `* filter=x` in `.gitattributes` ran the filter command under the exact command line localllm uses.

The agent can set this up. In a non-git project it can `git init`, add a filter to the new `.git/config`, write `.gitattributes` and change a file's timestamp. The next full-auto `run` then executes that command on the Mac, triggered by localllm itself rather than by the user's own git use (which the README already lists as a limit).

Run the check in a throwaway container instead: the base image, the project mounted read-only, `--network none`, the same hardening as the sandbox (host UID/GID, all capabilities dropped, `no-new-privileges`). The check needs no mask layout, so it can run before anything is written to the host. Whatever a filter does, it then runs offline, unprivileged and without write access to the project.

The prompt, the listed changes and the abort message stay the same. Non-git projects are still not asked.

## Acceptance criteria

- [ ] No host `git` command runs against the project's repository as part of the full-auto confirmation
- [ ] Integration test: a repo whose config sets a clean filter that writes a marker file; the confirmation runs, and no marker appears on the host
- [ ] Uncommitted changes inside the project (modified, staged, untracked) are still listed and require typing `y`; changes outside a subdirectory project are still ignored
- [ ] Non-git projects, and worktrees or submodule checkouts whose git dir is outside the project, behave as documented (no prompt, or a clear message)
- [ ] The check container gets no network and cannot write the project
- [ ] README updated

## Blocked by

None - can start immediately
