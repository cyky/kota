# DONE — 03 — Engine probes and refusal

Status: done

Resolved in: bc4096f

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

Before every **Session** of an engine project, probes run inside the **Engine VM** from a probe container. They must fail on an external IP, external DNS, the host's addresses and the LAN gateway. From the **Sandbox**, probes must succeed on the engine's Docker API and on a port published by a probe container. A failed probe blocks the **Session**, like a **Sandbox** probe. So does a VM that can't start, missing image archives, or a missing QEMU or **Engine base disk**, each with the reason and the fix.

`localllm check` shows the **Engine VM**'s status, declared images and probe results.

## Acceptance criteria

- [x] Engine probe set covers internet, DNS, host and LAN (must fail) and the API and a published port (must succeed)
- [x] Any engine probe failure, start failure, missing archive, or missing QEMU or **Engine base disk** refuses the **Session** with a fix
- [x] `check` shows engine status, images and probes
- [x] Unit tests for the engine probe set
- [x] Integration test: every engine probe passes on a real **Engine VM**
- [x] README probe list updated

## Blocked by

- `02-tracer-testcontainers-in-shell.md`

## Comments

**2026-10-15 — Done.** Verified on macOS (Apple silicon, QEMU 11.1.2, Rancher Desktop):

- Every shell session of an engine project runs 50 Engine VM probes on this Mac after the sandbox's. Inside the VM, from one forwarder-image container on the VM's network: the internet (IPv4, IPv6), DNS (the VM's resolver, `10.0.2.3`, `1.1.1.1`), and `10.0.2.2`, `10.0.2.3`, the host's own IPv4 addresses and the LAN gateway on the gateway port, the usual service ports and the host side of the VM's API and first published forward must all fail; a connection to the VM itself must succeed as a control. From the sandbox: the API with the session's certificate must answer, without one must be refused, and a port published by a probe listener in the VM must answer through `engine`. The probe image is `docker save`d from the host each session and removed with the listener afterwards.
- A failed engine probe, a VM not ready within 60 s, missing archives, a missing base disk or QEMU refuse the session with a fix. `check` shows the declared images with digests, why the VM can't start, and the engine probe results.
- A must-fail probe whose command is missing (exit 126 or 127) now counts as failed, in the sandbox probes too.
- Unit tests cover the engine probe set, run the rendered script with stub docker/socat/nslookup/curl, and cover `check`'s engine display. `TestEngineInShell` passed three runs in a row; its new subtests show every probe passing on a real VM, an open route (the VM's own address as a must-fail target) being detected, and `localllm check` as a subprocess. Full unit and Docker integration suites pass.

Found on the way: QEMU's port forwards cut off some connections in a burst of parallel new ones (8 of 40 parallel TLS handshakes to the API failed; 200 sequential ones all passed). The likely cause is a small listen backlog in QEMU's user-mode network; not confirmed. The sandbox-side engine probes now run one at a time, after the in-VM container. Users' tests that open many engine connections at once can still hit it; documented in the README as a limit. A host-side relay with a proper queue could fix it; not filed yet.

Not verified: the refusal for a missing QEMU (Homebrew QEMU is always on PATH here), and Linux (issue 09).
