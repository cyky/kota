# DONE — 01 — Load a project's .agents/skills when kota.yaml opts in

Status: done
Category: enhancement
Resolved in: df21eba

## What to build

Neither agent loads a project's `.agents/skills` today. opencode's `OPENCODE_DISABLE_PROJECT_CONFIG` turns off its whole project search, which also finds `.agents/` and `.claude/` skills (checked in the 2.0.25 binary: `ConfigDiscovery.discover` skips the walk when `config.project` is false). pi treats a project `.agents/skills` as a protected resource, and kota sets `defaultProjectTrust: never`.

A project opts in through `kota.yaml`:

```yaml
skills: true   # load .agents/skills, read-only in sessions
```

Without the key, sessions stay as they are. The project can only turn **Project skills** on; they are always read-only, unless the user starts the session with `--skills-writable`. A cloned repository can therefore load its skills, which is no more than its `AGENTS.md` can do, but can't let one session change what the next loads. When it is set, kota points each agent at that one folder in the config it renders, and nothing else from the project loads:

- opencode: `"skills": ["/workspace/.agents/skills"]` in the rendered `opencode.json`; `OPENCODE_DISABLE_PROJECT_CONFIG` stays.
- pi: `"skills": ["/workspace/.agents/skills"]` in the rendered `settings.json`; `defaultProjectTrust: never` stays.

By default, `.agents/skills` is a protected path like `kota.yaml`. It is mounted read-only over itself and locked on the host. A missing folder gets an empty read-only placeholder, so the agent can't create it. A symlink along the path is refused. So is a symlink inside the folder whose target isn't inside `.agents/skills` (a skill linked from `tools/skills/`, say): the agents follow links, and a read-only mount freezes the link but not its writable target. The error names the link; `--skills-writable` skips this check, since nothing is protected then. The probes check the mount. The folder is a directory mount, so its files aren't locked on the host: you can edit skills there during a session and the session sees the edits; replacing the whole folder drops the mount, which the end check already warns about. The agent isn't told the folder is read-only; the write error is enough, and kota's instructions stay as they are. This way a session can't plant a skill that a later session loads. With `--skills-writable`, the folder is an ordinary part of `/workspace`.

A read-only mount doesn't stop the agent from committing a changed skill without touching the working tree, and other agents on the host load `.agents/skills` too. So `.agents/skills/` joins the "Changed files that run code" at any depth, whatever `kota.yaml` says: the change report lists working-tree changes after a writable session or in a project that doesn't opt in, and warns about a skill changed only in a commit.

`run` and `shell` take `--skills-writable`; without `skills: true` they refuse it ("`--skills-writable` needs `skills: true` in `kota.yaml`"), because the flag would do nothing there. `check` doesn't take it: it reports `skills: off` or `skills: on, read-only (--skills-writable to let the agent edit them)` and probes the read-only mount. `prep` runs no agent and neither loads skills nor takes the flag.

A `--no-history` session takes `.agents/skills` from the project's working tree, as it does `kota.yaml`, so uncommitted skills load there too and `run` and `run --no-history` see the same skills. By default the copied folder is read-only, and the copy's excludes list it like the other kota files, so `git add -A` doesn't stage it. With `--skills-writable` it is ordinary files in the copy: the agent's skill changes come back only as commits on the **Returned branch**, and uncommitted host edits show as changes in the copy.

`skills` stays refused in the agent overrides, so a user-wide override can't load paths.

Fold in the docs gap found while scoping this. `docs/sessions.md` (Agent config) gives only permissions as the reason project config is ignored. It should say what a project config could do (permissions, plugins, MCP servers, commands, provider wiring), that a cloned repository could ship one as well as the agent writing it, and that opencode's single switch also drops `.agents/` and `.claude/` skills, while pi's trust covers `.agents/skills`. Then describe the opt-in.

## Acceptance criteria

- [x] `skills: true` in `kota.yaml` loads the project's `.agents/skills` in opencode and in pi; a value that isn't a boolean stops `run`, `shell` and `check` with the file and the value
- [x] Without the key, or with `false`, the rendered configs are unchanged
- [x] By default, `.agents/skills` (or its placeholder) rejects writes, and the probes check it; a symlink along the path, or one inside it whose target isn't inside `.agents/skills`, stops `run`, `shell` and `check` with the link (not with `--skills-writable`)
- [x] With `--skills-writable`, the agent can write `.agents/skills`; `kota.yaml` can't make it writable
- [x] `run` and `shell` refuse `--skills-writable` when `kota.yaml` doesn't have `skills: true`
- [x] A `--no-history` session loads `.agents/skills` from the working tree, uncommitted skills included; read-only and excluded from `git add -A` by default, ordinary files with `--skills-writable`
- [x] `.agents/skills/` is a watched path at any depth: the change report lists files added, modified or deleted under it, and warns when a commit changes one that the working tree doesn't
- [x] Project config, `.opencode/`, `.pi/`, pi extensions and `.claude/skills` still don't load
- [x] `kota check` shows whether project skills are off or on (read-only), and probes the read-only mount
- [x] An integration test starts each agent with a skill in `.agents/skills` and finds it listed
- [x] `docs/sessions.md`, `docs/protections.md`, `docs/dependencies.md` (kota.yaml keys) and `docs/agent-setup.md` describe the opt-in and why project config stays off; `docs/architecture.md` if package imports change

## Blocked by

None - can start immediately

## Comments

### 2026-10-22 (implementation)

> *This was generated by AI during implementation.*

A new `internal/projectskills` package reads the `skills` key (its `ConfigFile` is `deps.ConfigFile`, and `deps` accepts the key), checks the way to `.agents/skills` (`CheckPath`) and the links in it (`CheckLinks`). `mask.Plan` takes extra read-only directories. `agent.Configure` takes the in-sandbox skills directory, which both renderers list under `skills`. `run`, `shell` and `check` print `project skills:`.

Deviations:
- A missing `.agents/skills` is created empty for the session and removed afterwards if still empty, instead of a placeholder: a placeholder lands on the first missing parent, which made all of `.agents/` read-only.
- Absolute link targets in the folder are refused too: a host path doesn't resolve the same in the sandbox or a No-history copy. The refusal tells `check` and `prep` users to replace the link, since only `run` and `shell` take `--skills-writable`.
- A `kota.yaml` that can't be parsed doesn't stop a session, as for the dependencies: the skills don't load and the folder stays read-only, with a warning. `--skills-writable` is refused then.
- The watched path is anchored at the project root, like `.github/workflows/`, not at any depth.
- The No-history copy takes the skills in the clone's container, not from the host: replacing the folder from the host made the Docker VM mount its stale view of the deleted one (files missing, the read-only mount gone by the end). A link the commit holds at `.agents` or the folder is removed, not followed.
- In the copy, the user's uncommitted skills never become the session's work: read-only, tracked ones are marked skip-worktree, so `git add -A` and `git commit -a` leave them out; writable, `Clone` records the copied skills and `Return` counts only those the agent added, changed or deleted.
- `OPENCODE_DISABLE_CLAUDE_CODE` was removed: the opencode 2.0.25 binary doesn't contain it. `OPENCODE_DISABLE_LSP_DOWNLOAD` isn't in it either; left as is.

Verified: `go vet ./...` (also with `-tags integration`), `go test ./...`, and `go test -tags integration ./...` (31 packages ok). `TestProjectSkills` runs opencode and pi against a scripted gateway: the skill reaches the model only with the opt-in, `.opencode`, `.claude` and `.pi` skills never do, and writes to the folder fail unless `--skills-writable`. `TestProjectSkillsNoHistory`, `TestCloneTakesSkills` and `TestCloneSkillsStayTheUsers` cover the copy. Not verified: a real model in an interactive session, and Linux.
