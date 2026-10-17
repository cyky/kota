# DONE — 32 — Default agent instructions for task handling and unattended work, with a user override

Status: done
Category: enhancement

Resolved in: a9a099e

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

The agent gets no instructions from kota about how to work, only the Engine VM note when the project has one. Long tasks then depend on the agent's own habits and on context compaction: pi summarizes old messages when the context fills up (its defaults, unchanged by kota), and a local model's summaries lose detail, so a long **Session** drifts.

This issue gives every **Session**, for both pi and opencode, kota-wide instructions that keep the agent's state in the project's tickets instead of its context, plus an unattended mode the user switches on by telling the agent so. A user can replace the working style with their own file on the host. kota's facts about the **Sandbox** are always appended.

## Agent Brief

**Category:** enhancement
**Summary:** Always write the agent's instructions file: a built-in working style (or the user's override from `~/.config/kota/instructions.md`), followed by the **Sandbox** facts. Add `kota instructions` to print the default and say which file is in effect.

**Current behavior:**
- `cmd/kota/main.go` writes the agent's note (pi's `APPEND_SYSTEM.md`, opencode's `AGENTS.md` in its config dir) only when `sess.engineNote()` is non-empty, and mounts it read-only. Without an Engine VM the agent gets no note.
- kota has no user config dir. The only `XDG_CONFIG_HOME` lookup is for git's global ignore file.

**Desired behavior:**

1. **Three parts, in this order, in one file.**
   1. **Task handling** (always on).
   2. **Unattended mode** (applies only when the user's message asks for it).
   3. **Sandbox facts** (always appended by kota, never overridable): the **Session** is offline; dependency sources are local, so read them (`go doc`, `node_modules`, the module cache) instead of guessing APIs; a missing dependency can't be installed, so tell the user to add it to the manifest and run `kota prep` on the host; then the Engine VM note as today, when there is one.

   Parts 1 and 2 are one built-in text in `assets/` (for example `assets/agents/instructions.md`), the same for both agents.

2. **Default text.** Starting point, to be tightened while implementing (see `writing-for-agents` guidance: short, imperative, no filler):

   ```
   # Working on tasks

   - Work comes as tickets in the project's issue tracker. Check AGENTS.md for
     where it lives; otherwise use .scratch/<feature>/issues/NN-slug.md.
   - If you get a goal without tickets, first break it into tickets: thin
     vertical slices, each small enough to finish in one context window, each
     listing the tickets that block it.
   - Take one ticket from the frontier (ready-for-agent, all blockers done) and
     finish it before starting another.
   - The ticket is your memory. Tick acceptance criteria as they pass, and keep
     a short Notes section with decisions and dead ends. Your context is
     summarized when it fills up; re-read the ticket whenever you're unsure.
   - Work test-first where you can: one failing test, make it pass, repeat.
   - Commit after each green step. Don't repeat in notes what commits say.
   - A ticket is done when its criteria pass and the project's checks (tests,
     lint, build) pass. Then mark it done and commit. Report checks you couldn't
     run as not run, never as passed.

   # Working unattended

   Follow this only when the user tells you to work unattended, for example
   "work unattended", "AFK", "I'm away" or "don't wait for me".

   - Nobody will answer questions until the session ends. Don't ask; decide, or
     park the ticket.
   - When blocked, write the blocker in the ticket, set it to needs-info, and
     move to the next frontier ticket.
   - Stop when the frontier is empty, and summarize what was done, what was
     parked and why.
   ```

3. **Override.** `$XDG_CONFIG_HOME/kota/instructions.md`, with `~/.config/kota/instructions.md` when `XDG_CONFIG_HOME` isn't absolute (the same rule as the git ignore lookup; not `os.UserConfigDir`). If it exists, it replaces parts 1 and 2 for every project. Read it like the git ignore file: missing means the default; not a regular file, or larger than a fixed limit, is an error that stops startup with the path in the message. It's on the host and never mounted, so the agent can't change what later **Sessions** read.

4. **Always mounted.** `run` and `shell` write and mount the note on every **Session**, for both agents, read-only as today. The Engine-only special case goes. `check` verifies the note is read-only, as it does for the agent config.

5. **`kota instructions`.** Prints parts 1 and 2 of the file in effect to stdout, and to stderr which one it is: `built-in default`, or `override: <path>`. `kota instructions > ~/.config/kota/instructions.md` gives a starting point. `check` also shows which instructions are in effect.

6. **No new warning** about opencode without `--auto`. The startup line already shows the approval mode, and kota can't tell whether the user means to go AFK.

**Key interfaces:**
- `agentFor` and the note-writing block in `cmd/kota/main.go`: write the note always
- `session.engineNote` / `sandbox.EngineNote`: becomes the tail of part 3
- the `XDG_CONFIG_HOME` lookup in `cmd/kota/main.go` (global git ignore): share the dir resolution
- `assets`: the embedded default

**Acceptance criteria:**
- [x] Every `run` and `shell` **Session**, with or without an Engine VM, mounts the note read-only for pi and opencode, with parts 1, 2 and 3 in order
- [x] Without an override, parts 1 and 2 are the built-in text; with `~/.config/kota/instructions.md` present, they're its content, and part 3 is still appended
- [x] `XDG_CONFIG_HOME` is honored; a relative value falls back to `~/.config`
- [x] An override that is a directory, a non-regular file or too large stops startup with a message naming the path
- [x] The Engine VM note (available and `--no-engine`) still reaches the agent, unchanged
- [x] `kota instructions` prints the text in effect and names its source; `check` shows the source
- [x] Unit tests: note assembly (default, override, with and without Engine note), override lookup and errors
- [x] Integration test (fake HOME): a `shell` **Session** reads the note at the agent's path, can't write it, and sees the override's text when one is set
- [x] README: what the agent is told, the override path, `kota instructions`

**Out of scope:**
- A per-project override (`kota.yaml`) or a `--instructions` flag; project specifics go in the project's `AGENTS.md`
- A `--afk` flag; unattended mode is triggered by the user's message
- A deterministic loop that runs one fresh **Session** per ticket. Revisit after trying the prompt alone
- Tuning pi's compaction settings per model

## Blocked by

None (can start immediately).

## Comments

### 2026-10-17 (grilling)

> *This was generated by AI during a grilling session.*

Came out of a question about how pi handles long tasks with a limited local context window. pi auto-compacts at `contextWindow - reserveTokens` (default 16384) and keeps the last 20k tokens verbatim; the rest becomes a summary written by the session's own model. The answer chosen is to keep state outside the context: tickets as a task graph, each sized for one context window, progress ticked in the ticket, after Matt Pocock's `to-tickets` and `implement` skills.

Decisions:
- kota-wide, not per project: the rules come from kota's constraints (offline, **Gates** run by the agent, compaction), and a read-only mount keeps the agent from weakening them.
- Progress lives in the ticket, not a separate progress file.
- Task handling always applies; unattended mode only when the user says so in the conversation, with explicit trigger phrases so a small local model picks it up.
- The override replaces the working style only. The **Sandbox** facts stay, so an override can't drop the "container **Gates** can't run" note.
- Both agents get the same text.

Related change in the same session: `qwen3.5-2b-instruct`'s window was raised to 65536 (llama-swap `-c` and `assets/models.yaml`), because pi's fixed reserve of 16384 made its compaction threshold 0. The `unknown` fallback (8192) still has that problem for unlisted models; not addressed here.

### 2026-10-17

Implemented in a9a099e.

Verified:
- Unit tests (`internal/instructions`): note assembly with the default, an override, an empty override, and with and without the Engine VM note; `XDG_CONFIG_HOME` handling (absolute, relative, unset); a directory, FIFO, unreadable or oversized override is refused with its path.
- Command-level tests: the override replaces the working style in both agents' notes and keeps the Sandbox facts; a bad override stops startup before a session dir is created; `kota instructions` prints the text and names its source. `TestAgentEngineInstructions` now expects the note in every engine state, with parts in order.
- Integration test `TestInstructionsInShell` (test-only `XDG_CONFIG_HOME`): for opencode and pi, a `shell` Session reads the override plus the Sandbox facts at the agent's path, and writing, removing or moving the note fails. The probes now include the note as a read-only target. Full unit and Docker integration suites pass.

Changes from the brief:
- The Sandbox facts also say, at the user's request: for a missing package, name it with its version and reason, ask the user to add it on the host and run `kota prep`, and don't edit the manifest or lockfile (an out-of-date lockfile breaks the next prep, as in issue 17). Unattended, write the request in the ticket and park it.
- The default text was tightened slightly from the draft.

Not verified: the `check` output line naming the source has no assertion; it runs in the existing check integration tests only.
