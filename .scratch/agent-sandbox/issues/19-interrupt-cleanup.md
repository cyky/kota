# DONE — 19 — Clean up on Ctrl-C and ask before writing to the host

Status: done

Resolved in: 79a382d

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

localllm has no signal handling, so SIGINT or SIGHUP ends it immediately without any cleanup. Session preparation writes to the host (a seeded `.git/commondir`, the session directory) before the "Type y" prompts of full-auto `run` and of `prep`, and Ctrl-C at a prompt is a natural way to say no. An interrupt during Docker startup or the probes has the same effect. What gets left:

- the seeded `.git/commondir`, permanently: later sessions treat it as a real file and never remove it
- the forwarder container and the networks, until the next command for that project
- the session directory under the user cache dir
- no change report

Inside the agent's TTY session this is fine: the terminal is in raw mode, so Ctrl-C goes to the container.

Make an interrupt behave like declining or failing:

- Ask every confirmation (full-auto dirty tree, prep gate) before anything is written to the host or a container is started.
- While localllm or its child processes run, catch SIGINT/SIGHUP/SIGTERM and run the normal teardown (compose down, session cleanup, change report where a session ran, lease release), then exit non-zero.

## Acceptance criteria

- [x] Ctrl-C at the full-auto and prep prompts leaves no new files in the project, no session directory and no containers
- [x] An interrupt during Docker startup or the probes tears down the containers and networks and removes placeholders and seeded files
- [x] An interrupt during a non-TTY command (`run -- cmd` with stdin not a terminal) runs the same teardown and the change report
- [x] The lease is released, and the next command for the project starts without recovery
- [x] Test covering the prompt ordering (nothing written before the answer)
- [x] README updated where it describes what a killed process leaves behind

## Blocked by

- `18-dirty-tree-check-in-container.md` (same startup path; do it first to avoid conflicting edits)

## Comments

**2026-10-15 — Done.** New `internal/interrupt` package: the first SIGINT, SIGTERM or SIGHUP cancels a process-wide context, and later signals are ignored so teardown can finish. Docker step commands run with that context: they get SIGTERM, are abandoned after 10 s, and won't start once it's cancelled. `Session.Close` and crash recovery use plain commands, so teardown always runs. The full-auto and prep confirmations now come before `prepareSession`, which takes the confirmed dependency plan instead of detecting it again. All prompts (full auto, prep, clean) answer no on a signal. There's an explicit check before the agent starts and between prep's fetch and install. localllm prints `localllm: interrupted` and exits 130.

Verified with real signals: the test binary runs itself as localllm, with a fake `docker` and a fake HOME, and SIGINT goes to localllm only, not its process group. Ctrl-C at the prep prompt: exit 130, no docker calls, nothing left in the project or cache, and the lease is free with an empty record. Interrupt during the probes: `compose down` ran, the agent never started, and the seeded `.git/commondir`, placeholders and session dir were removed. Interrupt during a non-TTY `run -- cmd`: teardown ran and the change report listed the added file. With signal handling disabled, these tests fail with the old leftovers. Unit test for the interrupt package with a real SIGINT. `go vet`, the unit suite and the full integration suite pass.

Side effects: setup errors that `prepareSession` finds (a symlinked protection, a dependency directory overlapping a protected path) now appear after the full-auto question; an unknown agent, a broken models.yaml and a broken manifest are still caught before it. A second Ctrl-C during teardown is ignored; SIGKILL still leaves things behind, as the README says.
