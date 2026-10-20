# DONE — 41 — Engine VM forwards drop parallel connections

Status: done
Category: bug
Resolved in: 198c9b4

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The **Engine VM**'s ports reach the host through QEMU's user-mode network (`hostfwd=tcp:127.0.0.1:<host>-:<guest>` in `internal/engine/spec.go`). Its listener on the host drops connections that arrive together: dialing the Docker API's host port directly from the host, 4 of 5 parallel connects were reset, and with 20 and 50 most of them were (reset on connect, or broken pipe and reset during the TLS handshake). Inside the VM, dockerd's listen queue isn't the limit (`somaxconn` 4096). libslirp's own listen backlog is the likely cause; that hasn't been checked in its source.

In a **Session** this shows as `Client network socket disconnected before secure TLS connection was established` (`ECONNRESET`, `engine:2376`). The Docker CLI and Go's testcontainers open few connections at a time and pass. Node's testcontainers opens several at once: right after the container starts it follows the logs, checks the host port and runs an `exec` for the internal port check, and the `exec` is dropped, so `PostgreSqlContainer.start()` fails. From the agent, 7 or 9 of 20 parallel `/_ping`s failed the same way, while a single `exec`, a hijacked one, and three in parallel after the burst all passed. Published ports go through the same `hostfwd`, so a connection pool opening several connections to a database in the VM can hit it too.

Retrying in the engine forwarder doesn't help: `socat ... TCP:$host:$2,retry=20,interval=0.05` made no difference (11 of 20), because Docker's route to `host.docker.internal` accepts the connection before it dials the host port, so the forwarder never sees a failed connect.

Proposed fix, to be grilled: kota relays each forward on the host. It listens on a loopback port of its own (a normal backlog) for the session, and for each connection dials the `hostfwd` port, retrying a refused or reset connect with a short backoff for up to a second or two, then copies both ways. The route's `Target` ports become the relay's ports. The relay runs in the kota process for the session, like the VM itself.

## Acceptance criteria

- [x] 50 parallel TLS connections to the Docker API from the agent all succeed
- [x] 20 parallel connections to a published port all succeed
- [x] `node db.mjs` in `TestEngineInShell` (Node testcontainers, Postgres) prints `NODE-POSTGRES-QUERY-OK`, with no change to the project
- [x] The Go testcontainers test and the Engine VM probes still pass
- [x] The relay listens only on loopback, and stops with the **Session**
- [x] Tests use a fake HOME

## Blocked by

None - can start immediately.

## Comments

### 2026-10-20

Found while verifying issue 40 end to end: with the session user's passwd entry in place, Node's testcontainers got past `os.userInfo()` and then failed here. The Node check (`package.json`, `db.mjs` and the `npm install --package-lock-only` step in `engineProject`, plus `node db.mjs` in `TestEngineInShell`) was left uncommitted in `cmd/kota/engine_integration_test.go` for this issue.

### 2026-10-20 (findings while building)

libslirp's `tcpx_listen` calls `listen(s, 1)`: the backlog of one is confirmed in its source. The resets come on connect and also just after it, during the TLS handshake, before the VM sends anything, so the relay redials both and sends the client's bytes again, up to 64 KiB. Past that the connection goes on but isn't redialed. A clean close before any byte isn't redialed either: that's the target's answer, for example a published port nothing listens on.

With the relay alone, Node's testcontainers passed and 20 of 20 Postgres clients on a published port connected, but the API burst still lost 1 to 7 of 50 in two of four runs, with no failure seen by the relay. The second backlog was socat's default of 5 in the engine forwarder inside the sandbox. With `backlog=1024`, five runs in a row passed. The gateway forwarder had the same default and gets the same backlog.

### 2026-10-20 (implementation)

`internal/relay` is the relay; `enginesession` picks its ports next to QEMU's, points the route's targets at them, starts the relays with the VM and closes them on `Stop` and as soon as the VM exits. Deviations from the proposal: the relay also redials a connection reset after connecting but before the first byte back, sending the client's bytes again; the forwarders' socat backlog (both forwarders) is part of the fix; and the Engine VM probes also try the relay ports from the VM.

Verified: relay unit tests (redial after reset and refusal, giving up, no redial after an answer or a clean close, an upload past the replay limit, half-close, `Close` with a connection open), three times under `-race`; `TestEngineInShell` checks the Node testcontainers script, 50 parallel `/_ping`s over TLS and 20 parallel Postgres clients on a published port, and five runs in a row passed; the power-off subtest checks the relay ports refuse connections once the VM is off, and fails without that change. The full integration suite passed before the stop-on-exit change; after it, the power-off and testcontainers subtests and the `enginesession` unit tests under `-race` were rerun, not the whole suite. Not verified: a Linux host.
