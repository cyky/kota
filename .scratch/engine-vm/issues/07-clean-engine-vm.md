# 07 — Clean up the Engine VM

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

`localllm clean` lists and deletes the project's engine data (its image archives, its TLS certificates and anything else localllm keeps for its **Engine VM**) along with its volumes, after the same `y` confirmation. The **Engine VM** itself leaves no disk behind, since it runs on a throwaway layer over the shared **Engine base disk**, which `clean` doesn't touch. All engine data lives in localllm's own data directory, never inside the project.

## Acceptance criteria

- [ ] `clean` lists the project's archives and certificates and deletes them after `y`
- [ ] The shared **Engine base disk** is kept
- [ ] `clean` refuses while a **Session** of the project is running, as today
- [ ] Disks and archives are stored in localllm's data directory
- [ ] README updated

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
