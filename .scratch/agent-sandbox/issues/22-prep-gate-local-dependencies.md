# DONE — 22 — Prep gate: local and git dependencies run project code

Status: done

Resolved in: 2ddadc4

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The prep gate shows changes to any other project file only when the install runs project code. It looks for lifecycle scripts in the root and workspace `package.json` files, pnpmfiles, `yarnPath` and the `localllm.yaml` command. It misses dependencies that live in the project or are built from source:

- `file:`, `link:` and `portal:` dependencies outside the workspaces, whose own `package.json` has `preinstall`, `install`, `postinstall` or `prepare`. npm runs these during `npm ci`.
- git dependencies, whose `prepare` script runs during install.

In these cases the install runs code the agent may have changed, but other changed files are not shown before prep asks.

Count each of these as project code. Read the dependency fields of the root and workspace manifests (dependencies, devDependencies, optionalDependencies). List each local dependency that has a lifecycle script, and each git dependency, as a line in the prompt's "The install runs project code" section.

## Acceptance criteria

- [x] A `file:` dependency inside the project with a `postinstall` is listed as project code, and changed files are shown before prep asks
- [x] A git dependency is listed as project code
- [x] Registry dependencies and local dependencies without lifecycle scripts don't count
- [x] Unit tests for each case
- [x] README's list of what triggers the confirmation updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.** `deps.node` reads `dependencies`, `devDependencies` and `optionalDependencies` of the root and workspace manifests. Local dependencies (`file:`, `link:`, `portal:`, or a bare `./`/`../` path, inside the project) add their lifecycle scripts as hook lines, and their own local dependencies are followed, each package once. A local tarball (`file:vendor/x.tgz`) is listed as project code without being opened. Git dependencies (`git+`, `git:`, `github:`, `gitlab:`, `bitbucket:`, `gist:`, URLs ending in `.git`, and the `user/repo` shorthand) get a line each. Skipped: registry versions, `npm:` aliases, `workspace:`, remote tarball URLs, and local paths outside the project, which aren't in the sandbox. `link:` is listed when it has lifecycle scripts, as asked, although pnpm doesn't run them for links. `confirmPrep`'s comparison moved into `prepDecision` so it can be tested without stdin. README's list of what triggers the confirmation is updated.

Verified: `TestDependencyHooks` (every spec form, nested local deps, workspace overlap, skipped specs, and a project with only registry and script-free local deps getting no hooks) and `TestPrepGateLocalDependency` (real project and record: after changing `tools/tool/build.js`, prep asks and the prompt lists the postinstall and the changed file). `go vet`, the unit suite and the full integration suite pass.
