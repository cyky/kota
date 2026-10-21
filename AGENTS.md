# AGENTS.md

## Docs follow the code

A change ships with its docs, in the same commit:

- **Architecture**: a change that adds, removes or renames an `internal/` package, changes what one imports, or changes a command's flow, the runtime layout or where data lives updates `docs/architecture.md`. Done when every node and arrow in its Packages diagram matches `go list -f '{{.ImportPath}}: {{.Imports}}' ./internal/...`, and each diagram and table matches the new behaviour.
- **Behaviour**: a change users or the agent would notice updates the `docs/` page that describes it, and every other page that states the old behaviour (search `README.md`, `CONTEXT.md` and `docs/` for its terms).

## Agent skills

### Issue tracker

Issues and PRDs live as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.
