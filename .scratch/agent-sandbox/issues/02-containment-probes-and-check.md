# DONE — 02 — Containment probes and `check`

Status: done
Resolved in: `839c83e` (branch `issue-02-probes`)

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A containment probe suite that runs inside the real sandbox before every session, plus `localllm check [dir]`, which runs the probes and prints the resolved mounts and results without starting an agent.

- Must fail: external IP (`1.1.1.1:443`), external DNS resolution, the host gateway address (`host.docker.internal` / VM gateway) on arbitrary ports, the LAN gateway.
- Must succeed: gateway `/v1/models` through the forwarder; writing a file in `/workspace`.
- Short timeouts; whole suite about 1–2 s.
- `run` refuses to start when any probe fails and prints which; `--unsafe-skip-check` overrides with a loud warning.
- The probe list is extensible so later slices (git protection, masking) add their own probes.

## Acceptance criteria

- [x] `localllm check` prints mounts and per-probe pass/fail and exits non-zero on failure
- [x] `localllm run` runs the probes first and refuses to start on any failure
- [x] `--unsafe-skip-check` bypasses the probes with a warning
- [x] Integration tests (tagged; skipped when Docker is unavailable) build the real sandbox against a stub gateway on host loopback and assert every must-fail probe fails and every must-succeed probe succeeds
- [x] A test proves the probes detect breakage, e.g. a spec variant with the agent on a normal network makes the run refuse to start
- [x] Probe suite completes in about 2 s or less on this machine

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-13 — Done.** Implemented in `839c83e` on branch `issue-02-probes` (stacked on `issue-01-tracer-bullet`).

Verified on the host (Rancher Desktop, Docker 29.5.3):
- `localllm check` runs 55 probes; all pass; about 1 s including setup.
- `run` refuses to start when a probe fails (tested with a wrong gateway port); `--unsafe-skip-check` starts with a warning.
- Integration tests (`go test -tags integration ./...`): contained sandbox passes everything and the gateway port is probed directly on host addresses; a sandbox with the agent also on the normal network is detected.

Finding: the first run showed the Docker VM's sshd (port 22 on the internal network's gateway address) reachable from the agent. Docker's `internal` flag only blocks routing out. The internal network now uses isolated gateway mode (`com.docker.network.bridge.gateway_mode_ipv4=isolated`), so the VM has no address on it. Issue 01's commit alone had this gap.

Host targets are discovered at runtime: the forwarder's resolution of `host.docker.internal` and the gateway host, egress network gateways, Docker VM interfaces plus the first address of each subnet, and the macOS LAN gateway.

For later issues: add new probes to the probe suite (git protection in 04, masking in 05).
