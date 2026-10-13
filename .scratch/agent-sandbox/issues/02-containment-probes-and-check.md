# 02 — Containment probes and `check`

Status: ready-for-agent

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

- [ ] `localllm check` prints mounts and per-probe pass/fail and exits non-zero on failure
- [ ] `localllm run` runs the probes first and refuses to start on any failure
- [ ] `--unsafe-skip-check` bypasses the probes with a warning
- [ ] Integration tests (tagged; skipped when Docker is unavailable) build the real sandbox against a stub gateway on host loopback and assert every must-fail probe fails and every must-succeed probe succeeds
- [ ] A test proves the probes detect breakage, e.g. a spec variant with the agent on a normal network makes the run refuse to start
- [ ] Probe suite completes in about 2 s or less on this machine

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`
