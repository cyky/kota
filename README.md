# localllm

Run coding agents (opencode, later pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Network containment is verified before every session, but secret masking and git-hook protection are not in place yet. Don't point it at projects with secrets until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8080

## Usage

```sh
go install ./cmd/localllm
localllm build                         # build the sandbox images
localllm run [dir]                     # opencode in dir (default: current dir)
localllm run [dir] -- opencode run "…" # any command inside the sandbox
localllm check [dir]                   # show mounts and run the containment probes
```

Set the gateway with `--gateway URL` or `LOCALLLM_GATEWAY` (default `http://host.docker.internal:8080`).

## How it works

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The internal network runs in isolated gateway mode, so the Docker VM itself has no address on it. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.

Before every `run`, containment probes run in a fresh agent container. They try to reach the internet, external DNS, the host and Docker VM addresses (including the gateway port directly, bypassing the forwarder) and the LAN gateway, and check that the gateway and `/workspace` work. Any failure blocks the session; `--unsafe-skip-check` overrides that with a warning.

Integration tests need Docker: `go test -tags integration ./...`.
