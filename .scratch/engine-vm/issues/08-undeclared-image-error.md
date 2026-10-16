# DONE — 08 — Clear error for an undeclared image

Status: done

Resolved in: f40b966

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

When a test or the Docker CLI in a **Session** asks the **Engine VM** for an image that wasn't declared, the agent can tell which image it is and that it must be added under `engine.images` in `localllm.yaml` and prepped. The agent can then tell the user what to add, as it already does for dependencies.

Originally this asked for the failure itself to carry the message (an authorization plugin in the **Engine VM**). Reworded on 2026-10-16 to the lighter approach agreed with the user: the agent's instructions explain Docker's own failure. See Comments.

## Acceptance criteria

- [x] The agent's instructions list the declared images, describe how an undeclared image fails, and say to ask for it under `engine.images` in `localllm.yaml` and `localllm prep` (reworded; was: the failure message itself names it)
- [x] Nothing is downloaded: the pull fails at the registry lookup, since the **Engine VM** has no route out (reworded; was: the **Engine VM** never tries to download it)
- [x] Integration test with an undeclared image

## Blocked by

- `02-tracer-testcontainers-in-shell.md`

## Comments

### 2026-10-16

Approaches considered: an authorization plugin in the VM that refuses pulls with the exact message (in Python, a shell script, or Go). Each needs a base disk rebuild, and the Go one also needs a cross-compile and delivery step. The user chose the lightest option: explain the failure in the agent's note instead.

Measured in a real Session with the Engine VM (`redis:7.4-alpine` undeclared):
- Docker CLI: `Unable to find image 'redis:7.4-alpine' locally`, exit 125, within seconds.
- testcontainers-go: `generic container: create container: Error response from daemon: Get "https://registry-1.docker.io/v2/": dial tcp: lookup registry-1.docker.io: Temporary failure in name resolution`. It does not name the image.

So `sandbox.EngineNote` now lists the declared images and quotes these errors. It tells the agent to find the image in the failing test (or the testcontainers module's default image) when the error doesn't name it, not to retry or work around it, and to ask the user to declare it and run `localllm prep`.

Verified: `go test ./...` passes. The new integration subtest checks both error shapes and the note, and passed. The full `TestEngineInShell` run passed except this subtest, which then passed on its own after the fix; the full suite wasn't rerun after that.

Not done: the failure message itself still doesn't mention localllm. dockerd does attempt the pull, which fails on DNS. If that becomes a problem, the authorization plugin is the follow-up.
