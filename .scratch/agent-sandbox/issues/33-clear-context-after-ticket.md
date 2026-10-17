# DONE — 33 — Let pi clear its context after finishing a ticket

Status: done
Category: enhancement

Resolved in: 7ff892e

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

After issue 32, the agent keeps its state in tickets and commits, one ticket at a time. Its context still fills up across tickets, and pi's auto-compaction then summarizes it badly: the session's own local model writes the summary, tool results are cut to 2000 characters before summarizing, and the last 20k tokens, often the messiest part of the work, are kept verbatim.

This issue gives pi a tool that the agent calls when a ticket is done. It replaces the conversation so far with a short, fixed note that points to the tracker and the commits, so each ticket starts from a near-empty context inside the same **Session**. It's the in-session version of the "one fresh **Session** per ticket" loop that issue 32 leaves out of scope.

## Agent Brief

**Category:** enhancement
**Summary:** Ship a pi extension, mounted read-only in pi's agent dir, that registers a `ticket_done` tool. The tool triggers a compaction whose summary is written by the extension, not the model, and keeps as little of the old conversation as pi allows. Issue 32's default instructions tell the agent to call it after committing a finished ticket.

**Current behavior (after issue 32):**
- pi compacts only at `contextWindow - reserveTokens`, on overflow, or on the user's `/compact`. The model writes the summary.
- kota mounts `settings.json`, `models.json` and `APPEND_SYSTEM.md` read-only into pi's agent dir. It ships no extensions.

**Desired behavior:**

1. **The extension.** A TypeScript file in `assets/agents/pi/extensions/` (for example `ticket-done.ts`), mounted read-only at `<agent-dir>/extensions/`. pi loads agent-dir extensions without project trust. The base settings keep refusing project extensions (`defaultProjectTrust: never`).

2. **The tool.** `ticket_done`, with arguments for the ticket (path or number) and the commit that finished it. It:
   - validates the arguments (non-empty; the path is under `/workspace`); a bad call returns an error result and compacts nothing;
   - calls `ctx.compact()` and, in its own `session_before_compact` handler for that compaction only, returns a fixed summary instead of letting the model write one;
   - keeps as few old messages as pi allows. pi supports "retain-none" compactions (a compaction entry that records its own id as `firstKeptEntryId`); confirm whether an extension can produce one, through `session_before_compact` or by chaining a `compaction` entry from `turn_end`. If it can't, keep the smallest tail pi accepts, and say so in the closing comment.

   Summary text, roughly:

   ```
   Ticket <ticket> is done in commit <commit>. The previous conversation was
   cleared. Your state is in the project's tickets and git history, not here.
   Re-read the project's AGENTS.md and the tracker, then take the next ticket
   from the frontier. If the user told you to work unattended, that still
   applies.
   ```

   The last sentence matters: unattended mode (issue 32) is triggered by the user's message, which the compaction removes.

3. **Instructions.** Issue 32's default task-handling text gains one rule: after committing a finished ticket, call `ticket_done`. It goes in the part kota appends for pi only, since opencode has no such tool, so a user's override (issue 32) doesn't have to know about it, and opencode isn't told about a tool it lacks.

4. **Only real ticket boundaries.** The tool doesn't verify that the commit exists or that the ticket is marked done; the instructions say when to call it. It's a context tool, not a gate.

5. **Visible.** In interactive mode, a compaction made by the tool shows as one, so the user can see the context was cleared. The full transcript stays in the state volume, as with any compaction.

**Key interfaces:**
- pi's extension API (1.2.0): `pi.registerTool()`, `ctx.compact(options)` in tool context, `pi.on("session_before_compact")` returning `{ compaction: { summary, firstKeptEntryId, tokensBefore } }`, and `turn_end` entry chaining. See `docs/compaction.md`, `docs/extensions.md` and `examples/extensions/trigger-compact.ts` and `custom-compaction.ts` in the installed package
- `agentFor(pi.Agent)` in `cmd/kota/main.go`: add the extension to the read-only mounts
- issue 32's note assembly: the pi-only line

**Acceptance criteria:**
- [x] The extension is mounted read-only in every pi **Session**, and the agent can't write or replace it
- [x] Calling `ticket_done` replaces the conversation with the fixed summary; the model writes no summary for it
- [x] The kept tail is empty if pi allows it, otherwise the smallest pi accepts, and the closing comment says which
- [x] A bad call (missing argument, path outside `/workspace`) returns an error and compacts nothing
- [x] Auto-compaction at the threshold and `/compact` still behave as before (the handler only takes over its own compactions)
- [x] pi's instructions tell it to call `ticket_done` after committing a finished ticket; opencode's don't
- [x] Integration test (fake HOME): a pi **Session** loads the extension (the tool is listed), and the extension file is read-only. Driving a real model through a ticket isn't required.
- [x] README: what `ticket_done` does, and that it's pi only

**Out of scope:**
- opencode: no equivalent; it relies on its own `/compact`
- Starting a fresh **Session** per ticket from the host (the deterministic loop)
- Changing pi's threshold compaction settings
- Checking the commit or the ticket's status before clearing

## Blocked by

- `32-agent-instructions.md`

## Comments

### 2026-10-17 (grilling)

> *This was generated by AI during a grilling session.*

Came out of "compact seems useless sometimes". Checked against pi 1.2.0's installed docs and `dist/core/extensions/types.d.ts`: there's no built-in tool for the model to clear its context, and `newSession()` exists only in user-command context, not tool context. A tool's context does have `ctx.compact()`, and `session_before_compact` can supply the summary, so a clean cut at a ticket boundary is possible without the model's own summary. Not yet verified: whether an extension can make a retain-none compaction. That's the first thing to check.

### 2026-10-17

Implemented in 7ff892e.

Kept tail: empty. pi allows a retain-none compaction from an extension. `SessionManager.appendCompaction` records the entry's own id when `firstKeptEntryId` is null, and `buildContextEntries` then keeps no earlier entries. The extension doesn't use `ctx.compact()`. That call aborts the running turn and never continues it (`AgentSession.compact` starts with `await this.abort()`), so an unattended agent would stop after every ticket. Instead, the `turn_end` handler of the turn that called `ticket_done` returns a `compaction` boundary draft with `firstKeptEntryId: null` and `continue: true`. The note is the extension's own, so no summary request goes to the model. A turn that ended aborted or with an error clears nothing.

Verified:
- `TestTicketDone` (integration, scripted gateway, real pi `-p` in a shell Session):
  - `ticket_done` is offered to the model.
  - pi's instructions mention it.
  - After a valid call (path or number), the next request holds only the system prompt and the fixed note. There are two requests in total, so no summary request.
  - A missing argument, an absolute path outside `/workspace` or a `../` path returns the error and keeps the conversation.
  - The agent can't write `ticket-done.ts` or add a file to the extensions directory.
- `TestTicketDoneOnlyForPi`: the read-only, probed extensions mount and the instruction line are there for pi, also under an override, and absent for opencode.
- Full unit and Docker integration suites pass.

Changes from the brief:
- The whole `~/.pi/agent/extensions` directory is mounted read-only, not just the file, so no extension can be added beside it.
- A ticket number may be written `7` or `#7`.

Not verified:
- How the clear looks in pi's interactive UI. It is appended as a compaction entry, and the extension also shows a notice, but the TUI wasn't driven.
- That threshold compaction and `/compact` behave as before. This holds by construction, since the extension registers no `session_before_compact` handler, but nothing tests it.
