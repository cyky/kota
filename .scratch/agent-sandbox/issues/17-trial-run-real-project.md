# 17 — Trial run on a real project

Status: ready-for-human

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

A hands-on trial by the user, not code. Use opencode (ask mode, then `--auto`) and pi on a real TypeScript or Go project against the real llama-swap.

- Confirm tool calling works with `qwen3.6-coder` through the gateway (the `--jinja` question). If not, fix the llama-swap config and consider adding a tool-call probe to `check`.
- Confirm probes pass, masks hold, the approval flow and dirty-tree confirmation behave as intended.
- Note friction (prep loops, missing tools in the image, startup time) as new issues.

## Acceptance criteria

- [ ] One real task completed with opencode (ask mode) and one with pi
- [ ] Tool calling confirmed working, or a fix recorded
- [ ] Follow-up issues filed for any friction found

## Blocked by

- `10-pi-agent.md`
- `12-prep-dependencies.md`
- `14-shell-command.md`
