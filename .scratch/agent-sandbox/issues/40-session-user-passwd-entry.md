# DONE — 40 — The session user has a passwd entry

Status: done
Category: bug
Resolved in: 197f9ca

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A **Session** runs the **Agent** as the host UID:GID (`internal/sandbox/spec.go`, `agent.User`), but the base image only has the users of the `node` image. On macOS the host UID is usually 501, which has no entry in `/etc/passwd`, so anything that looks up the current user fails. Node's `os.userInfo()` throws `uv_os_get_passwd returned ENOENT`, and testcontainers calls it in `getReaper` even with `TESTCONTAINERS_RYUK_DISABLED=true`, so a project whose tests start containers on the **Engine VM** can't run them at all. `whoami`, ssh and Python's `getpass.getuser()` fail the same way. On a Linux host with UID 1000 the user resolves to `node` with `HOME=/home/node`, which doesn't match the session's `HOME`.

Give the session user a real entry, without changing the image or the hardening:

1. When it writes the session spec, kota generates a `passwd` and a `group` file in the session's state on the host. `passwd` has `root` (0:0, `/root`, `/bin/sh`) and `agent` with the host UID and GID, `HOME` set to `Home` (`/home/agent`) and shell `/bin/sh`. `group` has `root` (0) and `agent` with the host GID. The name is the fixed `agent`, not the host username, so nothing about the host leaks and no name needs escaping. If the host UID or GID is 0, the file has only the `root` entry for that ID, not a second one.
2. The **Agent** container bind-mounts them read-only over `/etc/passwd` and `/etc/group`. The root stays read-only, and no entrypoint or root step is added.
3. Prep runs the same way (it uses the same agent service), so tools that look up the user work there too.

The other containers (witness, volumes, forwarder, offline git containers) aren't changed.

## Acceptance criteria

- [x] In a **Session**, `node -e 'console.log(require("os").userInfo())'` prints `agent` with the host UID and GID and `homedir` `/home/agent`
- [x] `whoami` prints `agent` and `id` shows the group name `agent`
- [x] `/etc/passwd` and `/etc/group` are read-only in the **Agent** container
- [ ] A host UID of 1000 resolves to `agent`, not `node` (the generated contents are unit-tested; not run on a Linux host)
- [x] The spec tests cover the two mounts and the generated contents, including a host UID and GID of 0
- [ ] A project whose tests start a Postgres container with testcontainers (Ryuk disabled, as kota sets it) runs them in a **Session** with no change to the project; if something else then fails (for example reaching the mapped port through `TESTCONTAINERS_HOST_OVERRIDE`), it's written up as its own issue (the user lookup passes; the next failure is issue 41)
- [x] Tests use a fake HOME

## Blocked by

None - can start immediately.

## Comments

### 2026-10-20

Raised from a project whose scheduler tests start Postgres with testcontainers: the global setup failed in a **Session** with `SystemError: uv_os_get_passwd returned ENOENT` from `os.userInfo()` in testcontainers' `getReaper`, and no test ran. The project's own handoff suggested reading a `TEST_DB_HOST` override or skipping the tests; both work around a gap in kota, and any tool that looks up the current user hits it. Considered and not chosen: baking the user into the image with a build arg (per-user image, collides with `node` at UID 1000), a root entrypoint appending to `/etc/passwd` (needs a writable `/etc` and a root step), `nss_wrapper` (needs the same generated file plus an `LD_PRELOAD` in every process), and running as `node` (wrong file owners on a Linux host).

### 2026-10-20 (implementation)

`sandbox.Accounts` writes the two files, and `prepareSession` puts them in the session dir and mounts them read-only with the git excludes, so run, shell and prep all get them, and the read-only probe covers both. Deviation: the witness container also gets the two mounts, since it takes the agent's volumes; it runs nothing that reads them. Verified: before, `docker run --user 501:20 kota-base` gave `whoami: cannot find name for user ID 501` and the handoff's `uv_os_get_passwd returned ENOENT`; `TestSessionUserHasName` (integration, real container) now gets `agent`, the host UID and GID and `/home/agent` from `whoami`, `id -gn` and `os.userInfo()`. Not verified on a Linux host with UID 1000; the unit test covers the contents for that case.

End to end, Node's testcontainers now gets past the user lookup, then fails on a separate problem: the Engine VM's port forwards drop parallel connections. That is issue 41. The full integration suite passes except for that check: a Node testcontainers Postgres script (`db.mjs`) in `TestEngineInShell`, next to the Go test. It isn't committed with this issue, and goes in with issue 41 as its acceptance check.
