# 01 — Read-only mounts and masks vanish over files replaced on the host

Status: ready-for-agent
Category: security

## Parent

Found while closing `.scratch/offline-git/issues/01-offline-git-module.md`: `TestInstructionsInShell/pi` failed intermittently on the containment probes.

## What happens

On Rancher Desktop (1.24, `vmType: vz`, `mountType: virtiofs`), the Docker VM caches file lookups in the shared project for about a second. When a file that kota mounts read-only over itself is deleted, or replaced by a new file, on the host within that second before a Session starts:

1. Docker resolves the mount point through the VM's stale cache entry and mounts over the old, deleted file.
2. When the entry expires, the VM kernel drops the stale entry, and the read-only mount on it with it. `/proc/self/mountinfo` no longer lists it.
3. The path then shows the host's current state: missing, or the new file, **writable** from the Sandbox.

The probes run right after the container starts, so they can pass before step 2. The protection is gone afterwards.

Ways a protected file gets replaced on the host:

- kota's own cleanup deletes the placeholders (`.envrc`, `.kotaignore`, `kota.yaml`, `kota.Dockerfile` when missing) and the seeded `.git/commondir`. The next Session's Prepare re-seeds `commondir`, a new file. Two Sessions back to back on one project hit this: that is the test failure.
- Host tools replace files by writing a new one and renaming it over the old: host git for `.git/config`, many editors on save. A Session started within about a second after that gets the same result.
- Not checked: `--no-history`'s `TakeConfig` removes and re-creates kota's files in the copy right after the clone container has read them.

## Experiments

Each trial: a container stats `w/.envrc` (the VM caches it), the host changes the file, a new container mounts an empty file read-only over `w/.envrc`, and probes it 2s later. Five trials each, all with the same result:

| Host change before the mount | Probe at 2s | Mount listed |
|---|---|---|
| delete | missing | no |
| none (file kept) | read-only | yes |
| edit in place (`>>`) | read-only | yes |
| delete and re-create | **writable** | no |
| replace by rename | **writable** | no |
| delete, then stat or `ls` from another container | missing | no |
| re-create, then stat from another container | **writable** | no |
| delete, wait 0.5s | missing | no |
| delete, wait 1s / 1.5s / 2s / 3s | read-only | yes |
| re-create, wait 1s / 1.5s | read-only | yes |
| rename or delete, probe at once, then at 2s | read-only, then **writable** / missing | no |

So:

- Re-creating or renaming is worse than deleting: the container can write the host file.
- A lookup from another container doesn't clear the stale entry. Only time does, about 1s here.
- Masked and read-only files that stay unchanged, or are edited in place, are fine.
- A probe right after start can't see it.

The 1s comes from Lima's virtiofs cache on this machine; kota doesn't control it. Not checked: Docker Desktop, Colima, OrbStack, native Linux (no virtiofs, probably unaffected).

Script (bash, run from outside the repo):

```bash
probe='if [ ! -e /w/.envrc ]; then echo MISSING; elif (: >> /w/.envrc) 2>/dev/null; then echo WRITABLE; else echo ok; fi'
d=$(mktemp -d ~/Library/Caches/kota-stale-XXXX); mkdir "$d/w" "$d/s"; : > "$d/s/empty"; : > "$d/w/.envrc"
docker run --rm -v "$d/w:/w" kota-base:latest stat /w/.envrc > /dev/null
echo new > "$d/w/.envrc.tmp"; mv "$d/w/.envrc.tmp" "$d/w/.envrc"   # the host change
c=$(docker run -d --network none -v "$d/w:/w" -v "$d/s/empty:/w/.envrc:ro" kota-base:latest sleep 30)
docker exec "$c" sh -c "$probe"; sleep 2; docker exec "$c" sh -c "$probe"
docker exec "$c" grep -c ' /w/.envrc ' /proc/self/mountinfo
docker rm -f "$c" > /dev/null; rm -rf "$d"
```

## Fix directions (decided in triage: see the agent brief under Comments)

- Probe again after the cache window (and at the end of the Session) instead of only at start. This catches it but doesn't prevent it.
- Wait out the window before `compose up` when kota itself just changed a protected path (cleanup, seeds, `TakeConfig`). This covers kota's own changes, not host tools'. The length depends on the engine's settings.
- Don't delete placeholders and seeds in cleanup, so nothing changes between Sessions. This leaves empty files in the project.
- Mount protections so they don't depend on the mount point's entry in the shared filesystem, for example from a directory kota owns outside the project. Needs research.
- Check the inode of each protected path in the container against the host's after the window, and refuse on a mismatch.

## Acceptance criteria

Replaced in triage; the agent brief under Comments repeats them with context.

Integration (Docker, throwaway projects under `~/Library/Caches`):

- [ ] Two Sessions back to back on one project both pass the probes, and `.envrc`, `.git/commondir` and a mask are still in place 3s into the second
- [ ] During a Session, replacing a protected file and a masked file on the host by rename fails with "Operation not permitted", and both mounts are still in place 3s later
- [ ] A protected file replaced by rename just before a Session is read-only in that Session
- [ ] After a Session, no file in the project has `uchg` left, and placeholders and seeds are removed as before
- [ ] `TestInstructionsInShell` passes with `-count=10`

Unit:

- [ ] A lock list left by a killed Session is unlocked by the registry's recovery before the next Session plans its mounts
- [ ] A file that already had `uchg` before the Session is still locked after it
- [ ] Only regular files inside the project are locked or unlocked: symlinks, directories, paths outside the project and edited lock-list entries are skipped
- [ ] The pre-start check refuses after its timeout and names the paths that differ; the timeout can be set in tests
- [ ] The end check warns for each protection mount that is gone, with different text for masks and read-only paths

Docs:

- [ ] `docs/protections.md` covers the locks, what fails on the host during a Session, how to unlock after a crash, and that whole directories aren't locked
- [ ] Other engines are written down as unchecked, and Linux as not supported yet (`02-host-replace-on-linux.md`)

## Blocked by

None.

## Comments

### 2026-10-19 (investigation)

> *This was generated by AI during an investigation.*

The flaky probe failure predates the offline-git change: at 5c912bf it failed 8 of 12 runs, with offline-git 4 of 12, and later 0 of 6 once runs were spaced out. An instrumented probe showed the paths missing at probe time, while the mounts were intact in passing runs.

### 2026-10-19 (triage)

> *This was generated by AI during triage.*

Further experiments (Rancher Desktop, throwaway dirs under `~/Library/Caches`, `postgres:17` as the container since `kota-base` was not present):

- **During a Session too.** With a container running, replacing the read-only-mounted `.envrc` on the host by rename made it writable from the container 3s later, and the mount was no longer listed. Waiting or probing before start can't cover this.
- **Masks too.** With `.env` masked in a running container, replacing it on the host by rename let the container read the new content 3s later. This breaks confidentiality, not only integrity. In-place edits keep both protections.
- **The stale entry is visible to a fresh container.** Right after a host rename and delete, new containers saw the old size and the deleted file for about 0.5s, then the host's state. Inode numbers in the VM differ from the host's, so a comparison has to use existence, size and mtime.
- **`chflags uchg` on the host holds.** With `.env` and `.git/config` locked, a host rename over them, `rm` and `git config` all failed with "Operation not permitted", and both mounts were still listed 3s later.
- **Linux has the same problem without a cache.** On a Docker volume on the VM's own disk, a rename from another container over a masked file succeeded and the mask was gone at once. Linux isn't supported yet; split out as `02-host-replace-on-linux.md`.

Decided with the maintainer: lock instead of relying on timing; lock files only; record locks before setting them and recover them through the session registry, with no helper process; check before start that the VM's view matches the host's, with a 10s limit; check at the end that every protection mount is still there; apply to every command that uses the mask layout. macOS only.

## Agent Brief

**Category:** security (bug)
**Summary:** Lock protected and masked project files on the host for the length of a Session, wait before start until the Docker VM's view of them matches the host's, and warn at the end if a protection mount is gone.

**Current behavior:**
kota protects project files by bind mounts over their paths: read-only mounts over themselves (git hooks and config, `commondir`, git config includes, `.envrc`, `.kotaignore`, `kota.yaml`, `kota.Dockerfile`), empty read-only placeholders where they're missing, seeded files (`commondir`), and masks (an empty file or directory over each secret). When the file at such a path is replaced (rename over it) or deleted on the host, the mount in the Sandbox is dropped:

- on a virtiofs engine (Rancher Desktop with VZ) about 1s later, when the VM's cached entry for the path expires;
- if the replace happened within about 1s before the Session, Docker mounts over the stale entry, and the mount is dropped shortly after the probes passed.

A dropped read-only mount makes the host file writable from the Sandbox; a dropped mask shows the real secret. kota's own cleanup causes the before-start case: it deletes placeholders and seeds after each Session, so two Sessions back to back lose them (this is `TestInstructionsInShell`'s flaky failure). Host git (lock file plus rename for `.git/config`) and editors that save by rename cause both cases.

**Desired behavior:**

1. *Locks.* Before the Sandbox starts, kota sets the user-immutable flag (`chflags uchg`) on every regular file in the project's mount plan: read-only files, file placeholders, seeded files and masked files. kota creates file placeholders on the host itself (empty) so they can be locked, instead of leaving it to Docker. Directories are never locked (masked directories, read-only directories such as hooks, directory placeholders, pinned directories).
   - Each path is appended to a per-project lock list, kept next to the project's session-registry record, *before* its flag is set, so a crash never leaves an unrecorded lock.
   - A file that already has `uchg` is left out of the list, so kota never clears a lock the user set.
   - Before locking or unlocking, a path is checked again: a regular file, not a symlink, with no symlink on the way, inside the project. Anything else is skipped, including lock-list entries that fail this.
   - Session start prints a note with the count, e.g. `note: 9 protected and masked files are locked on the host until the session ends`.
2. *Unlock.* Normal cleanup (including after Ctrl-C, SIGTERM, SIGHUP and startup errors) clears the flags on the listed files, then removes the list, before removing placeholders and seeds. The session registry's recovery for a crashed Session (the step that already stops orphaned containers and the Engine VM) does the same with a list it finds, before the new Session plans its mounts. So any later kota command on the project unlocks it.
3. *Pre-start check.* After locking and before the Sandbox starts, one helper container (offline, the project mounted read-only, as other helper containers are) stats every path in the mount plan in a loop, every 250ms, and compares with the host: exists or not, size, and mtime (to the nanosecond if the VM shows it, else to the second). It stops when all match. If they don't match within 10s (a named value, settable in tests), the Session refuses to start and names the paths that differ. Since the files are locked, a match holds for the Session.
4. *End check.* Before teardown, while the Sandbox still runs, kota reads its mount table and checks that every protection and mask mount is still there. For each one that's gone it prints a `WARNING:`. For a mask: the agent may have read the secret. For a read-only path: it was writable for part of the Session, so review it. A warning, not a block, and it doesn't change the exit status.
5. *Commands.* All commands that build a Session from the mask layout: `run`, `shell`, `check`, `prep`, and `--no-history` Sessions (on the No-history copy). Done in the shared session setup and cleanup, so no command can skip it.
6. *Platform.* Locks are macOS only. kota doesn't support Linux yet. Don't add Linux code paths beyond keeping the build working.

**Key interfaces:**
- `mask.Layout` and its `Mount` (fields `Placeholder`, `Seed`, `Mask`, `Dir`, `Pin`): the mount plan that decides what's locked. Its `Prepare` and `Cleanup` are where seeding and removal happen today; placeholder creation moves to kota.
- The session registry's `Recover` hook: gains unlocking a leftover lock list.
- The session setup and cleanup shared by run, shell, check and prep, and `lifecycle.Startup` (start order, probes and teardown): the pre-start check runs before the Sandbox starts; the end check runs before teardown.
- The probes still run at start as now.

**Acceptance criteria:**
See the issue's Acceptance criteria section; it was rewritten in this triage and is the list to tick.

**Out of scope:**
- Linux (`02-host-replace-on-linux.md`).
- Locking directories, and protecting against a whole protected or masked directory being replaced on the host. Document it as a limit.
- A helper process that unlocks after SIGKILL.
- Changing which paths are protected or masked.
- Checking Docker Desktop, Colima and OrbStack. Document them as unchecked.
- `--ro` paths and kota's own config mounts: they're mounted outside the project's shared mount and aren't affected.
