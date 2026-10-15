# DONE — 25 — Probe and build polish

Status: done

Resolved in: 85ba688

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Small fixes found in the pre-trial review, done together:

1. **Pinned probe and mountinfo escapes.** The pinned probe compares paths with the mount point field of `/proc/self/mountinfo`, which the kernel escapes (space as `\040`, also tab, newline, backslash). A pinned path with a space fails the probe even when the mount is right. Decode the field before comparing.
2. **IPv6 egress probes.** All network probes use IPv4. Add must-fail probes for an external IPv6 address (e.g. `2606:4700:4700::1111` port 443) and for IPv6 DNS lookups (`getent ahostsv6`), so enabling IPv6 in Docker can't open a route unnoticed.
3. **Pin images by digest.** The base and forwarder Dockerfiles use version tags (`node:24-trixie-slim`, `golang:1.27-trixie`, `alpine:3.22`). Pin each `FROM` to a digest, keeping the tag in a comment, so builds are reproducible as the PRD asks.
4. **Usage text.** In `localllm` usage, the `clean [flags] [dir]` line is one space short, so its description doesn't line up with the others.

## Acceptance criteria

- [x] Pinned probe passes for a pinned path containing a space (unit test on the decoding, integration test with such a path)
- [x] IPv6 internet and DNS probes run, must fail, and fail on the current sandbox
- [x] Base and forwarder images build from digest-pinned `FROM` lines
- [x] Usage text aligned
- [x] README probe list updated

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.**
1. The pinned probe escapes the path as the kernel writes mountinfo (`probe.MountinfoEscape`: space, tab, newline and backslash as octal) and compares it with the raw field. The path now goes to awk through the environment, since `awk -v` interprets backslashes and would have broken those paths too.
2. New must-fail probes: `internet [2606:4700:4700::1111]:443` and `internet dns ipv6 example.com` (`getent ahostsv6`).
3. `NODE_IMAGE`, `GO_IMAGE` and `ALPINE_IMAGE` default to `tag@sha256:<index digest>` (today's registry digests), so the tag stays on the line instead of in a comment; Docker uses the digest. A comment says how to bump them.
4. The `clean` usage line is aligned.

README probe description mentions IPv4/IPv6 and AAAA lookups.

Verified: `TestMountinfoEscape`; `TestPinnedProbeRuns` runs the real probe command against a mountinfo fixture with escaped paths. Integration `TestPinnedPathWithSpace` (`core.hooksPath` = `git hooks/_`, so `/workspace/git hooks` is pinned) passes, and both IPv6 probes ran and failed; without the escaping it fails on the real sandbox. Both Dockerfiles built from the pinned digests under throwaway tags, which were then removed; the user's `localllm-base` and `localllm-forwarder` images weren't rebuilt. `go vet`, the unit suite and the full integration suite pass.
