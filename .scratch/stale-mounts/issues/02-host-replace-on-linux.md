# 02 — On Linux, replacing a protected file on the host drops its mount at once

Status: needs-triage
Category: security

## Parent

`.scratch/stale-mounts/issues/01-stale-virtiofs-mounts.md`, which fixes this on macOS by locking protected files on the host (`chflags uchg`) for the Session. kota doesn't run on Linux yet; this is for when it does.

## What happens

Since Linux 3.18, deleting a file, or renaming another over it, succeeds when the file is a mount point in another mount namespace, and the kernel detaches the mount there. The Sandbox is another mount namespace than the host's. So on a Linux host, when a host tool replaces a file kota mounts read-only or masks (host git writing `.git/config` through `config.lock`, an editor saving by rename), the Sandbox loses that mount immediately:

- a read-only protection becomes writable from the Sandbox;
- a mask shows the real file, so the agent can read the secret.

There is no cache window as on virtiofs: it happens at once, so no wait or probe before the Session helps, and a watch would always be too late.

## Experiment

Run inside Rancher Desktop's Linux VM, on a Docker volume (the VM's own disk, no virtiofs), with a second container standing in for the host:

```bash
v=kota-stale-test; docker volume create "$v"
docker run --rm --network none --entrypoint sh -v "$v:/w" postgres:17 -c 'echo SECRET > /w/.env; : > /w/empty; chmod 444 /w/empty'
c=$(docker run -d --network none --entrypoint sleep -v "$v:/w" --mount "type=volume,src=$v,dst=/w/.env,volume-subpath=empty,readonly" postgres:17 60)
docker exec "$c" cat /w/.env                       # empty: masked
docker run --rm --network none --entrypoint sh -v "$v:/w" postgres:17 -c 'echo NEW > /w/.env.tmp && mv /w/.env.tmp /w/.env'
docker exec "$c" cat /w/.env                       # NEW: mask gone
docker exec "$c" grep -c ' /w/.env ' /proc/self/mountinfo   # 0
docker rm -f "$c"; docker volume rm "$v"
```

Result: the mask held before the rename and was gone right after it (`NEW`, not listed). Not checked on a native Linux host.

## Fix directions (undecided)

- Lock the files with `chattr +i`. It needs `CAP_LINUX_IMMUTABLE`, so root: a privileged helper or `sudo`, which kota otherwise avoids.
- Refuse to start on Linux, or start only with an explicit flag that accepts the risk, until there is a lock.
- Something other than bind mounts over project paths for protections and masks. Needs research.

A watch that stops the Session when a protected path changes on the host doesn't close it: the mount is gone before the watch sees the change.

## Acceptance criteria

- [ ] Reproduced on a native Linux host
- [ ] A fix direction is decided, or Linux sessions refuse to start with protections that can be dropped
- [ ] `docs/protections.md` describes the behaviour on Linux

## Blocked by

- `.scratch/stale-mounts/issues/01-stale-virtiofs-mounts.md`
- Linux support for kota as a whole (no issue yet; `.scratch/engine-vm/issues/09-engine-vm-on-linux.md` covers only the Engine VM)

## Comments
