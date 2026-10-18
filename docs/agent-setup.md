# The agents

What kota tells opencode and pi, how to set your own preferences for them, and pi's `ticket_done` tool. How their config is kept away from the project is in [Sessions](sessions.md#agent-config).

## What the agent is told

Every `run` and `shell` session gives the agent kota's instructions, the same for both agents: opencode as a global `~/.config/opencode/AGENTS.md`, pi as `APPEND_SYSTEM.md` in its agent directory. Both are mounted read-only, and `check` verifies that. The project's own `AGENTS.md` still loads as well, so put project specifics there. The instructions have three parts, in this order:

1. **Working on tasks** (built in, `assets/agents/instructions.md`). Work from the project's tickets, one at a time, and keep progress and decisions in the ticket rather than the agent's context, which is summarized when it fills up. Work test-first, commit after each green step, and report checks that couldn't run as not run.
2. **Working unattended** (built in). Applies only when you tell the agent so in the conversation ("work unattended", "AFK", "I'm away", "don't wait for me"). The agent doesn't ask questions: when blocked, it writes the blocker in the ticket, sets it to needs-info and moves on. It stops when no tickets are left and summarizes.
3. **This sandbox** (always added by kota). The session is offline. Dependencies are local, so it reads their source instead of guessing APIs. Nothing can be installed, so for a missing package it names the package, version and reason, and asks you to add it on the host and run `kota prep`. It doesn't edit the manifest or lockfile for it, since a lockfile it can't update breaks the next `prep`. pi is also told to call `ticket_done` (see below), and in an engine project the [Engine VM](engine-vm.md) note follows.

### Your own working style

To replace parts 1 and 2 for every project, write your own to `~/.config/kota/instructions.md` (`$XDG_CONFIG_HOME/kota/instructions.md` when that is set to an absolute path). Part 3 is still added, so an override can't drop the sandbox facts. `kota instructions > ~/.config/kota/instructions.md` gives you a starting point: it prints parts 1 and 2 as they are in effect, and says on stderr whether they are the `built-in default` or `override: <path>`. `check` shows the source too.

The file is read on the host and never mounted, so the agent can't change it. If it isn't a regular file, or is larger than 64 KiB, `run`, `shell` and `check` stop with its path.

## Your own agent settings

Keybindings, theme and similar preferences can be set for every session in every project with an **agent override**: a file under `~/.config/kota/` (or `$XDG_CONFIG_HOME/kota/`) in the agent's own format.

| File | Agent setting | Allowed keys |
|---|---|---|
| `opencode/cli.json` | opencode v2's terminal preferences (`~/.config/opencode/cli.json`) | `keybinds`, `leader`, `theme`, `animations`, `cursor`, `mouse`, `scroll`, `prompt`, `session`, `tabs`, `diffs`, `terminal`, `mini`, `highlights` |
| `opencode/opencode.json` | opencode's config (`~/.config/opencode/opencode.json`) | `compaction` |
| `pi/settings.json` | pi's settings (`~/.pi/agent/settings.json`) | `defaultThinkingLevel`, `modelThinkingLevels`, `thinkingBudgets`, `hideThinkingBlock`, `steeringMode`, `followUpMode`, `doubleEscapeAction`, `treeFilterMode`, `compaction`, `branchSummary`, `retry`, `theme`, `quietStartup`, `tuiMode`, `fullscreenExitOutput`, `fullscreenScrollbar`, `fullscreenCopyOnSelect`, `fullscreenWheelScrollLines`, `editorPaddingX`, `outputPad`, `autocompleteMaxVisible`, `showHardwareCursor`, `terminal`, `images`, `markdown` |
| `pi/keybindings.json` | pi's keybindings (`~/.pi/agent/keybindings.json`) | every action; each value must be a key or a list of keys (empty to disable the action) |

For example, to move opencode's session delete off `ctrl+d`, put this in `~/.config/kota/opencode/cli.json`:

```json
{
  "$schema": "https://opencode.ai/v2/cli.json",
  "keybinds": {
    "session.delete": "ctrl+shift+d"
  }
}
```

An override is read on the host when the session starts, merged over the bundled config (objects key by key, other values replaced), and never mounted, so the agent can't change it. Edits reach the next session. `run` and `check` print `agent override:` with the files in use, or `none (bundled config)`. Without an override, the bundled config is used as is.

Only the allowed keys can be set; `$schema` is accepted and dropped. Any other key, invalid JSON, a file that isn't a regular file or one over 64 KiB stops `run`, `shell` and `check` with the file and the key. Keys are refused when they would load code or files, run commands, change trust, telemetry or the network route, or change what kota sets itself:

### opencode/cli.json

Reaches opencode through `OPENCODE_CLI_CONFIG_CONTENT`, which opencode keeps authoritative over its own `cli.json`; opencode has no project-level `cli.json`, so the project can't change these. Settings you change in opencode's TUI go to the session's own `cli.json` and are lost after it, so put lasting ones in the override.

Refused: `plugins` (loads code), `attention` (takes sound file paths, and notifications can't leave the sandbox), `debug` and `experimental`.

### opencode/opencode.json

Merged between the bundled config and what kota sets (the gateway provider, models and `--auto` permissions), so `share: disabled`, `autoupdate: false` and the permissions stay. `compaction` is accepted in opencode 2's form (`auto`, `keep.tokens`, `buffer`) or v1's.

Refused, under their v1 or v2 names:

- `keybinds` and `theme`, which opencode 2 reads from `cli.json`: use that override;
- keys that run or load code: `plugin`, `mcp`, `lsp`, `formatter`, `command`, `agent`, `shell`;
- keys that read paths: `instructions`, `watcher`, `skills`, `references`, `worktree`;
- the gateway wiring and the approval model: `provider`, `model`, `small_model`, `enabled_providers`, `permission`, `tools`, `policies`, `websearch`;
- `share`, `autoupdate`, `server` and `experimental`.

`opencode debug config` in `kota shell` shows the config opencode reads.

### pi/settings.json

Merged between the bundled settings and the provider and model kota sets, so `--model` still picks the model, and the bundled `defaultProjectTrust: never`, disabled telemetry and disabled llama.cpp extension stay.

Refused:

- anything that loads files or code: `packages`, `extensions`, `skills`, `prompts`, `themes` (so `theme` names a built-in theme);
- anything that runs commands: `shellPath`, `shellCommandPrefix`, `npmCommand`, `externalEditor`;
- trust, telemetry and the network route: `defaultProjectTrust`, `enableInstallTelemetry`, `enableAnalytics`, `httpProxy`, `transport`;
- what kota sets: `defaultProvider`, `defaultModel`, `enabledModels`, `defaultTools`, `sessionDir`.

### pi/keybindings.json

Rendered into pi's agent directory and mounted read-only, like `settings.json`; without an override it is empty and pi uses its defaults. pi has no project-level keybindings. `/hotkeys` in pi shows the bindings in effect.

## Clearing pi's context between tickets

pi has a `ticket_done` tool; opencode relies on its own `/compact`. The agent calls it with the ticket and the commit once a ticket is done and committed. When that turn ends, the conversation is replaced by a short fixed note: the ticket is done in that commit, the agent's state is in the tickets and git history, so re-read `AGENTS.md` and the tracker, take the next ticket, and keep working unattended if you asked for that. pi then carries on with the next ticket in the same session. In the interactive UI the clear shows as a compaction, and the full transcript stays in the state volume.

Why: pi's own compaction, which runs when the context fills up or on `/compact`, has the model summarize the old messages. With a small local model, that summary loses detail, so a clean cut at each ticket boundary works better. Automatic compaction and `/compact` still work as before.

The tool isn't a gate. It doesn't check that the commit exists or that the ticket is marked done; the instructions say when to call it. A call without both arguments, or with a ticket path outside `/workspace`, returns an error and clears nothing.

The tool is a pi extension in `assets/agents/pi/extensions/ticket-done.ts`. kota mounts that directory read-only at `~/.pi/agent/extensions`, so the agent can't change the extension or add one beside it, and `check` verifies that. The project's `.pi/extensions` stay untrusted.
