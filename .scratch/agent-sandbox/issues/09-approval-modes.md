# 09 — Approval modes: ask by default, --auto, dirty-tree confirmation

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- opencode asks for approval by default (its normal permission prompts).
- `--auto` renders opencode permissions that approve edits and bash commands.
- Starting a full-auto session on a project with uncommitted changes asks the user to type `y`. Anything else aborts and suggests committing or stashing. A non-git project skips the check.
- The confirmation logic is agent-aware so pi (always full auto, slice 10) reuses it.

## Acceptance criteria

- [ ] Default `run` → opencode prompts before edits and commands
- [ ] `run --auto` → opencode acts without prompts
- [ ] `run --auto` on a dirty tree asks for `y`; declining aborts without starting the container
- [ ] Clean tree or ask mode → no confirmation
- [ ] Unit tests for the rendered permissions (ModelCatalog/config renderer) and for the confirmation decision

## Blocked by

- `08-model-catalog.md`
