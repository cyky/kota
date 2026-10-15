# DONE — 17 — Trial run on a real project

Status: done

Resolved in: 6da0a84, 8e937eb

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A hands-on trial by the user, not code. Use opencode (ask mode, then `--auto`) and pi on a real TypeScript or Go project against the real llama-swap.

- Confirm tool calling works with `qwen3.6-coder` through the gateway (the `--jinja` question). If not, fix the llama-swap config and consider adding a tool-call probe to `check`.
- Confirm probes pass, masks hold, the approval flow and dirty-tree confirmation behave as intended.
- Note friction (prep loops, missing tools in the image, startup time) as new issues.

Step-by-step checklist: `.scratch/agent-sandbox/trial-run-checklist.md`.

## Acceptance criteria

- [x] One real task completed with opencode (ask mode) and one with pi
- [x] Tool calling confirmed working, or a fix recorded
- [x] Follow-up issues filed for any friction found

## Blocked by

- `10-pi-agent.md`
- `12-prep-dependencies.md`
- `14-shell-command.md`
- `18-dirty-tree-check-in-container.md`
- `19-interrupt-cleanup.md`
- `20-tracked-masks-readable-from-git.md`
- `21-broken-manifest-blocks-run.md`
- `22-prep-gate-local-dependencies.md`
- `23-git-config-project-references.md`
- `24-watch-more-files-that-run-things.md`
- `25-probe-and-build-polish.md`

## Comments

**2026-10-15 — Done.** Trial run by the user on a personal TypeScript project (the real repo rather than a fresh clone) against llama-swap with `qwen3.6-coder`. Results are in `.scratch/agent-sandbox/trial-run-checklist.md`.

Verified: all 71 probes pass, including the IPv6 ones. `.env` reads as empty in the agent session and in the shell. Hook and git config writes fail, there's no internet, and the gateway answers. Prep asks on first run and on manifest changes, and not otherwise. opencode completed a task in ask mode (it asked before `npm test`) and one in full auto, after the uncommitted-changes check. pi completed a task too. Tool calling works with both agents, so no llama-swap fix is needed. Ctrl-C at a prompt exits with 130 and leaves nothing behind. `clean` removes the project's volumes.

Follow-up issues: 26 (agent commits capture mask placeholders), 27 (change report says what changed), 28 (output polish). Recorded in the checklist but not filed: Ctrl-C after the agent starts exits with 1 instead of 130; a cold model makes the first message hang with no message; the `.npmrc` warning says "secret" for a settings-only file.

Not verified: the step 8 check that `localllm shell` opens with a broken `package.json` and has the last prep's `node_modules`. The warning itself was seen with `localllm run`. Agent changes made to the project during the trial (a commit, edits, the test `.env`) were discarded afterwards.
