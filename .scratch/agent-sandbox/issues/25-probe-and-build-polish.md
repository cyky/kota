# 25 — Probe and build polish

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Small fixes found in the pre-trial review, done together:

1. **Pinned probe and mountinfo escapes.** The pinned probe compares paths with the mount point field of `/proc/self/mountinfo`, which the kernel escapes (space as `\040`, also tab, newline, backslash). A pinned path with a space fails the probe even when the mount is right. Decode the field before comparing.
2. **IPv6 egress probes.** All network probes use IPv4. Add must-fail probes for an external IPv6 address (e.g. `2606:4700:4700::1111` port 443) and for IPv6 DNS lookups (`getent ahostsv6`), so enabling IPv6 in Docker can't open a route unnoticed.
3. **Pin images by digest.** The base and forwarder Dockerfiles use version tags (`node:24-trixie-slim`, `golang:1.27-trixie`, `alpine:3.22`). Pin each `FROM` to a digest, keeping the tag in a comment, so builds are reproducible as the PRD asks.
4. **Usage text.** In `localllm` usage, the `clean [flags] [dir]` line is one space short, so its description doesn't line up with the others.

## Acceptance criteria

- [ ] Pinned probe passes for a pinned path containing a space (unit test on the decoding, integration test with such a path)
- [ ] IPv6 internet and DNS probes run, must fail, and fail on the current sandbox
- [ ] Base and forwarder images build from digest-pinned `FROM` lines
- [ ] Usage text aligned
- [ ] README probe list updated

## Blocked by

None - can start immediately
