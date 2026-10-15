# 07 — Clean up the Engine VM

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

`localllm clean` lists and deletes the project's **Engine VM** and its image archives along with its volumes, after the same `y` confirmation. The **Engine VM**'s disks and the archives live in localllm's own data directory, never inside the project.

## Acceptance criteria

- [ ] `clean` lists the **Engine VM** and archives and deletes them after `y`
- [ ] `clean` refuses while a **Session** of the project is running, as today
- [ ] Disks and archives are stored in localllm's data directory
- [ ] README updated

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
