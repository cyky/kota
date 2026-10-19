# 01 — Read-only mounts vanish over files replaced just before a Session

Status: needs-triage
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

## Fix directions (undecided)

- Probe again after the cache window (and at the end of the Session) instead of only at start. This catches it but doesn't prevent it.
- Wait out the window before `compose up` when kota itself just changed a protected path (cleanup, seeds, `TakeConfig`). This covers kota's own changes, not host tools'. The length depends on the engine's settings.
- Don't delete placeholders and seeds in cleanup, so nothing changes between Sessions. This leaves empty files in the project.
- Mount protections so they don't depend on the mount point's entry in the shared filesystem, for example from a directory kota owns outside the project. Needs research.
- Check the inode of each protected path in the container against the host's after the window, and refuse on a mismatch.

## Acceptance criteria

- [ ] An integration test runs two Sessions back to back on one project and passes
- [ ] An integration test replaces a protected file by rename just before a Session, and the Session either keeps it read-only or refuses to start
- [ ] `TestInstructionsInShell` passes repeatedly (`-count=10`)
- [ ] The behaviour on other engines is checked or written down as unchecked

## Blocked by

None.

## Comments

### 2026-10-19 (investigation)

> *This was generated by AI during an investigation.*

The flaky probe failure predates the offline-git change: at 5c912bf it failed 8 of 12 runs, with offline-git 4 of 12, and later 0 of 6 once runs were spaced out. An instrumented probe showed the paths missing at probe time, while the mounts were intact in passing runs.
