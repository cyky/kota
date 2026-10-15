# 20 — Say that tracked masked files can be read from git history

Status: ready-for-agent

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A masked file reads as empty inside the sandbox, but when it is tracked in git, or was ever committed, its content is in `.git/objects`. `git show HEAD:.env` prints the real secret. Today's warning only says that a commit made in the sandbox would record the file emptied, which suggests the secret itself is safe.

Change the warning shown by `run`, `shell`, `check` and `prep` for a tracked masked path so it says the content can be read through git, and how to fix it: remove the file from the index and from history, and rotate the secret. Update the README's masking limits to match. Detecting files that were committed only in the past is out of scope; say in the README that history isn't checked.

## Acceptance criteria

- [ ] The tracked-mask warning says the content is readable through git inside the sandbox, as well as the existing emptied-commit point
- [ ] The README masking limits say that tracked or previously committed secrets are exposed through git history, and what to do about it
- [ ] Existing mask tests updated for the new wording

## Blocked by

None - can start immediately
