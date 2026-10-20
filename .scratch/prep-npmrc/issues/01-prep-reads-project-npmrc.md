# 01 — Prep reads the project-root .npmrc once approved; Sessions never do

Status: ready-for-agent
Category: enhancement

## What to build

Projects that install from a private registry keep its auth in the project-root `.npmrc`. Today `.npmrc` is masked everywhere, **Prep** included, so `npm ci`, `pnpm fetch` and `yarn install` (classic) can't authenticate. The only workaround is `!.npmrc` in `.kotaignore`, which also lets the agent read the token in every **Session**.

After this change:

- **Prep** mounts the real project-root `.npmrc` read-only, in place of its mask, in both the Fetch and Install phases. Install scripts can read it, but no step can change what the next **Prep** uses.
- Only the project root's file. Nested `.npmrc` files stay masked in **Prep**. kota never reads the host's `~/.npmrc` or global npm config, and passes no host env vars, so `${NPM_TOKEN}`-style references resolve to empty.
- **Prep** refuses a root `.npmrc` that is a symlink, or isn't a regular file, before any container starts. The error names the file and, for a symlink, its target. Docker resolves symlinks on the host, so without this check an agent could create `.npmrc -> ~/.npmrc` (or any host file) in a **Session** and have the next **Prep** mount that file with network access. The refusal matches the one for a symlinked `kota.Dockerfile`.
- Approval reuses the existing Prep gate:
  - When a root `.npmrc` exists, the Prep prompt adds a line saying it is visible to this **Prep**, install scripts included, and that tokens **Prep** shouldn't see belong in the host's `~/.npmrc`, which kota never reads.
  - The Prep record stores whether `.npmrc` exposure was approved. A record without that approval, including every record written before this change, makes **Prep** ask even when nothing else changed. After an approval, an unchanged project and `.npmrc` mean no prompt, as today. A changed `.npmrc` already asks, since it is a Prep input.
- **Sessions** (`run`, `shell`, `check`) always mask every `.npmrc`, root and nested. `!.npmrc` (or any `.kotaignore` rule that would unmask a `.npmrc`) no longer takes effect, and kota warns that it is ignored and that **Prep** already reads the root file. This also applies to a **No-history copy**.
- There is no switch to keep `.npmrc` from **Prep**. If that's needed later, it goes in `kota.yaml` (`prep.npmrc: false`), not `.kotaignore`.

Decided in a grilling session on 2026-10-20; the rule is recorded in `CONTEXT.md` (Relationships and Flagged ambiguities).

## Acceptance criteria

- [ ] With a root `.npmrc` holding a registry and token, `kota prep` sees its exact contents in both phases (for example, a Fetch and an Install step that print a hash of it), and a write to it from either phase fails
- [ ] A nested `packages/foo/.npmrc` still reads as empty in **Prep**
- [ ] Nothing from the host's `~/.npmrc`, global npm config or env reaches **Prep** (test with a fake HOME holding its own `.npmrc` and a set `NPM_TOKEN`)
- [ ] A root `.npmrc` that is a symlink (to a file inside or outside the project), a directory or a fifo makes `kota prep` fail before any container starts, naming the file and, for a symlink, its target
- [ ] With a root `.npmrc`, the Prep prompt includes the visibility line; without one, it doesn't
- [ ] A Prep record from before this change, with a root `.npmrc` and nothing else changed, makes `kota prep` ask; after `y`, the next unchanged `kota prep` doesn't ask
- [ ] A project prepped without a root `.npmrc` that gains one asks on the next `kota prep`
- [ ] In `run`, `shell` and `check`, root and nested `.npmrc` read as empty and can't be written, with and without `!.npmrc` in `.kotaignore`; with it, kota prints a warning that it is ignored
- [ ] `kota check` shows the root `.npmrc` as masked in Sessions and readable by **Prep**
- [ ] `docs/dependencies.md`, `docs/protections.md` and `docs/sandbox.md` describe the new behavior: **Prep** reads the root `.npmrc` once approved, only that file, no host config or env; symlinks refused; Sessions always mask it; `!.npmrc` ignored with a warning; a token **Prep** shouldn't see goes in the host `~/.npmrc`. The old "unmask it in `.kotaignore`" advice is removed
- [ ] Tests use a fake HOME, never real `~` paths

## Blocked by

None - can start immediately

## Comments
