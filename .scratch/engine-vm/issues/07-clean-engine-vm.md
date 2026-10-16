# DONE — 07 — Clean up the Engine VM

Status: done

Resolved in: c058052

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

`localllm clean` lists and deletes the project's engine data (its image archives, its TLS certificates and anything else localllm keeps for its **Engine VM**) along with its volumes, after the same `y` confirmation. The **Engine VM** itself leaves no disk behind, since it runs on a throwaway layer over the shared **Engine base disk**, which `clean` doesn't touch. All engine data lives in localllm's own data directory, never inside the project.

## Acceptance criteria

- [x] `clean` lists the project's archives and certificates and deletes them after `y`
- [x] The shared **Engine base disk** is kept
- [x] `clean` refuses while a **Session** of the project is running, as today
- [x] Disks and archives are stored in localllm's data directory
- [x] README updated

## Blocked by

- `02-tracer-testcontainers-in-shell.md`

## Comments

### 2026-10-16

`clean` now lists and, after `y`, removes the project's engine directory (`localllm/engine/projects/<hash>/` under the user cache dir: image archives and their record, listed by image) and any of the project's session directories under `localllm/sessions/` left by sessions that didn't end cleanly. It doesn't parse `localllm.yaml`.

Certificates: unlike the PRD's sketch, they are made per session in the session directory and deleted when the session ends, so nothing persistent exists per project. `clean` covers the ones a killed localllm leaves behind by removing those session directories, matched by exact name so a project whose name extends this one's is untouched.

Verified: `go test ./...` and `go vet` (also with `-tags integration`) pass. The new `TestCleanEngineData` covers decline, confirm, the kept base disk, another project's data and session dir, and the follow-up "nothing to clean". Refusal during a session is unchanged and covered by `TestCleanRefusesActiveProject`; the project lease also stops an orphaned VM first.

Not verified: no integration run of `clean` against a real prepped engine project; the unit test uses a fake HOME and a fake `docker`.
