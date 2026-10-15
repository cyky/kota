# DONE — 20 — Say that tracked masked files can be read from git history

Status: done

Resolved in: 1f44904

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A masked file reads as empty inside the sandbox, but when it is tracked in git, or was ever committed, its content is in `.git/objects`. `git show HEAD:.env` prints the real secret. Today's warning only says that a commit made in the sandbox would record the file emptied, which suggests the secret itself is safe.

Change the warning shown by `run`, `shell`, `check` and `prep` for a tracked masked path so it says the content can be read through git, and how to fix it: remove the file from the index and from history, and rotate the secret. Update the README's masking limits to match. Detecting files that were committed only in the past is out of scope; say in the README that history isn't checked.

## Acceptance criteria

- [x] The tracked-mask warning says the content is readable through git inside the sandbox, as well as the existing emptied-commit point
- [x] The README masking limits say that tracked or previously committed secrets are exposed through git history, and what to do about it
- [x] Existing mask tests updated for the new wording

## Blocked by

None - can start immediately

## Comments

**2026-10-15 — Done.** The tracked-mask warning in `internal/mask` now says the mask doesn't hide the file from git, so its content can be read inside the sandbox (`git log -p -- <path>`), keeps the emptied-commit point, and says to remove it from the index (`git rm --cached`) and from history, and rotate the secret. `git log -p` is the example rather than `git show HEAD:<path>` because it also prints content for masked directories such as `.aws/`. `run`, `shell`, `check` and `prep` all print it through `printWarnings`. The README masking limits say tracked or ever-committed secrets are readable through `.git`, that history isn't checked, and how to fix it (`git rm --cached`, `git filter-repo`, rotate).

Verified: `TestPlanWarnsAboutTrackedMasks` checks the new wording; `go vet` and the unit suite pass. The integration suite wasn't run, since no integration test checks this wording and only the message text changed.
