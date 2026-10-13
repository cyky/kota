# localllm

Run coding agents (opencode, later pi) in an offline Docker sandbox that can reach only the local model gateway (llama-swap).

Status: early. Containment probes, secret masking and git-hook protection are not in place yet. Use only with throwaway projects until they land (see `.scratch/agent-sandbox/`).

## Requirements

- Docker with compose (tested with Rancher Desktop on macOS)
- A model gateway on the host, by default llama-swap on port 8080

## Usage

```sh
go install ./cmd/localllm
localllm build                         # build the sandbox images
localllm run [dir]                     # opencode in dir (default: current dir)
localllm run [dir] -- opencode run "…" # any command inside the sandbox
```

Set the gateway with `--gateway URL` or `LOCALLLM_GATEWAY` (default `http://host.docker.internal:8080`).

## How it works

The agent container is attached only to an internal Docker network with no route out. A forwarder container joins that network and a normal one, and relays a single port to the gateway. Inside the sandbox the gateway is `http://gateway:8080`. The project is mounted read-write at `/workspace`, and the agent runs as your UID with all capabilities dropped.
