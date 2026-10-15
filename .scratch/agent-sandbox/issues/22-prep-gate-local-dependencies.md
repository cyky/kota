# 22 — Prep gate: local and git dependencies run project code

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The prep gate shows changes to any other project file only when the install runs project code. It looks for lifecycle scripts in the root and workspace `package.json` files, pnpmfiles, `yarnPath` and the `localllm.yaml` command. It misses dependencies that live in the project or are built from source:

- `file:`, `link:` and `portal:` dependencies outside the workspaces, whose own `package.json` has `preinstall`, `install`, `postinstall` or `prepare`. npm runs these during `npm ci`.
- git dependencies, whose `prepare` script runs during install.

In these cases the install runs code the agent may have changed, but other changed files are not shown before prep asks.

Count each of these as project code. Read the dependency fields of the root and workspace manifests (dependencies, devDependencies, optionalDependencies). List each local dependency that has a lifecycle script, and each git dependency, as a line in the prompt's "The install runs project code" section.

## Acceptance criteria

- [ ] A `file:` dependency inside the project with a `postinstall` is listed as project code, and changed files are shown before prep asks
- [ ] A git dependency is listed as project code
- [ ] Registry dependencies and local dependencies without lifecycle scripts don't count
- [ ] Unit tests for each case
- [ ] README's list of what triggers the confirmation updated

## Blocked by

None - can start immediately
