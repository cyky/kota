# 41 — Engine VM forwards drop parallel connections

Status: needs-triage
Category: bug

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The **Engine VM**'s ports reach the host through QEMU's user-mode network (`hostfwd=tcp:127.0.0.1:<host>-:<guest>` in `internal/engine/spec.go`). Its listener on the host drops connections that arrive together: dialing the Docker API's host port directly from the host, 4 of 5 parallel connects were reset, and with 20 and 50 most of them were (reset on connect, or broken pipe and reset during the TLS handshake). Inside the VM, dockerd's listen queue isn't the limit (`somaxconn` 4096). libslirp's own listen backlog is the likely cause; that hasn't been checked in its source.

In a **Session** this shows as `Client network socket disconnected before secure TLS connection was established` (`ECONNRESET`, `engine:2376`). The Docker CLI and Go's testcontainers open few connections at a time and pass. Node's testcontainers opens several at once: right after the container starts it follows the logs, checks the host port and runs an `exec` for the internal port check, and the `exec` is dropped, so `PostgreSqlContainer.start()` fails. From the agent, 7 or 9 of 20 parallel `/_ping`s failed the same way, while a single `exec`, a hijacked one, and three in parallel after the burst all passed. Published ports go through the same `hostfwd`, so a connection pool opening several connections to a database in the VM can hit it too.

Retrying in the engine forwarder doesn't help: `socat ... TCP:$host:$2,retry=20,interval=0.05` made no difference (11 of 20), because Docker's route to `host.docker.internal` accepts the connection before it dials the host port, so the forwarder never sees a failed connect.

Proposed fix, to be grilled: kota relays each forward on the host. It listens on a loopback port of its own (a normal backlog) for the session, and for each connection dials the `hostfwd` port, retrying a refused or reset connect with a short backoff for up to a second or two, then copies both ways. The route's `Target` ports become the relay's ports. The relay runs in the kota process for the session, like the VM itself.

## Acceptance criteria

- [ ] 50 parallel TLS connections to the Docker API from the agent all succeed
- [ ] 20 parallel connections to a published port all succeed
- [ ] `node db.mjs` in `TestEngineInShell` (Node testcontainers, Postgres) prints `NODE-POSTGRES-QUERY-OK`, with no change to the project
- [ ] The Go testcontainers test and the Engine VM probes still pass
- [ ] The relay listens only on loopback, and stops with the **Session**
- [ ] Tests use a fake HOME

## Blocked by

None - can start immediately.

## Comments

### 2026-10-20

Found while verifying issue 40 end to end: with the session user's passwd entry in place, Node's testcontainers got past `os.userInfo()` and then failed here. The Node check (`package.json`, `db.mjs` and the `npm install --package-lock-only` step in `engineProject`, plus `node db.mjs` in `TestEngineInShell`) was left uncommitted in `cmd/kota/engine_integration_test.go` for this issue.
