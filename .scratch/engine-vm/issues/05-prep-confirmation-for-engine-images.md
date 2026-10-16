# DONE — 05 — Prep confirmation and digest record for Engine images

Status: done

Resolved in: 2829b8e

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

After a successful **Prep**, the digest each **Engine image** tag resolved to is recorded with the prep record. The next **Prep** downloads only new images and images whose tag moved. Its `y` confirmation lists added, removed and moved images. A failed download names the image.

## Acceptance criteria

- [x] Digests recorded after a successful **Prep**
- [x] Only new or moved images are downloaded
- [x] Added, removed and moved images are listed in **Prep**'s confirmation
- [x] A failed download names the image
- [x] Unit tests for EngineImages: first prep, unchanged, added, removed, moved tag, record after success

## Blocked by

- `02-tracer-testcontainers-in-shell.md`

## Comments

**2026-10-16 — Done.** Verified on macOS (Apple silicon, Rancher Desktop):

- After a successful prep, `images.json` in the project's archive directory records each image with the digest it was pulled by (already true since issue 02; now written from `engine.Plan`'s record).
- Each later prep looks up, with crane in a throwaway container, where the recorded tags point now. Only images in the last prep's host-side record are looked up before the confirmation, so a reference an agent added to `localllm.yaml` reaches the network only after the user typed `y` (unit test with a fake docker and crane).
- prep asks when the images changed, also when no file did (a moved tag), and lists them as new, moved (short old and new digest) or removed. Only new images, moved ones (pulled by the digest shown) and ones whose archive is gone are downloaded; the rest are hard-linked into the new archive directory, which replaces the old one after every download succeeded. If every image was removed, the archive directory is deleted.
- A failed lookup or download writes the image to a `failed` file, and the error names it; the previous archives and record stay.
- Unit tests: `engine.Plan` (first prep, unchanged, added, removed, moved tag, archive gone), the record after success (including a download pulled by an unplanned digest), the lookup and fetch scripts against a stub crane (including the failed image named), and the prompt text. Integration against the real registry: a second prep downloaded nothing and kept the same archive inode; a bad image (`localhost:1/missing:1`) failed by name and kept the archive and record. Full unit tests and the CLI integration package pass.

Not verified: a real moved tag (a registry tag can't be moved from here; covered by unit tests and the fake-crane CLI test), and the interactive prompt typed by hand (covered through the printing function). Every prep of an engine project now does a lookup before the prompt, about a second.
