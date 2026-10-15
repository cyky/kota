# 19 — Clean up on Ctrl-C and ask before writing to the host

Status: ready-for-agent

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

- [ ] Ctrl-C at the full-auto and prep prompts leaves no new files in the project, no session directory and no containers
- [ ] An interrupt during Docker startup or the probes tears down the containers and networks and removes placeholders and seeded files
- [ ] An interrupt during a non-TTY command (`run -- cmd` with stdin not a terminal) runs the same teardown and the change report
- [ ] The lease is released, and the next command for the project starts without recovery
- [ ] Test covering the prompt ordering (nothing written before the answer)
- [ ] README updated where it describes what a killed process leaves behind

## Blocked by

- `18-dirty-tree-check-in-container.md` (same startup path; do it first to avoid conflicting edits)
