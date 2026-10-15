# 05 — Prep confirmation and digest record for Engine images

Status: ready-for-agent

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

After a successful **Prep**, the digest each **Engine image** tag resolved to is recorded with the prep record. The next **Prep** downloads only new images and images whose tag moved. Its `y` confirmation lists added, removed and moved images. A failed download names the image.

## Acceptance criteria

- [ ] Digests recorded after a successful **Prep**
- [ ] Only new or moved images are downloaded
- [ ] Added, removed and moved images are listed in **Prep**'s confirmation
- [ ] A failed download names the image
- [ ] Unit tests for EngineImages: first prep, unchanged, added, removed, moved tag, record after success

## Blocked by

- `02-tracer-testcontainers-in-shell.md`
