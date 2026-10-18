# The agents

What kota tells opencode and pi, how to set your own preferences for them, and pi's `ticket_done` tool. How their config is isolated from the project is in [Sessions](sessions.md#agent-config).

## What the agent is told

Every `run` and `shell` session gives the agent kota's instructions, the same for both agents. opencode gets them as a global `~/.config/opencode/AGENTS.md`, and pi as `APPEND_SYSTEM.md` in its agent directory. Both are mounted read-only, and `check` verifies that. The project's own `AGENTS.md` still loads as well, so put project specifics there. The file has three parts, in this order:

1. **Working on tasks** (built in, `assets/agents/instructions.md`). Work from the project's tickets, one at a time, and keep progress and decisions in the ticket rather than the agent's context, which is summarized when it fills up. Work test-first, commit after each green step, and report checks that couldn't run as not run.
2. **Working unattended** (built in). Applies only when you tell the agent so in the conversation ("work unattended", "AFK", "I'm away", "don't wait for me"). The agent doesn't ask questions: when blocked, it writes the blocker in the ticket, sets it to needs-info and moves on. It stops when no tickets are left and summarizes.
3. **This sandbox** (always added by kota). The session is offline. Dependencies are local, so it reads their source instead of guessing APIs. Nothing can be installed, so for a missing package it names the package, version and reason, and asks you to add it on the host and run `kota prep`. It doesn't edit the manifest or lockfile for it, since a lockfile it can't update breaks the next `prep`. In an engine project, the Engine VM note follows (see [Engine VM](engine-vm.md)).

To use your own working style, write it to `$XDG_CONFIG_HOME/kota/instructions.md` (`~/.config/kota/instructions.md` when `XDG_CONFIG_HOME` is unset or not an absolute path). It replaces parts 1 and 2 for every project. Part 3 is still added, so an override can't drop the Sandbox facts. The file is read on the host and never mounted, so the agent can't change it. If it isn't a regular file, or is larger than 64 KiB, `run`, `shell` and `check` stop with its path.

pi alone is also told to call `ticket_done` after it commits a finished ticket (see below). That line goes in part 3, so an override doesn't need to know about it, and opencode, which has no such tool, isn't told about it.

`kota instructions` prints parts 1 and 2 as they are in effect to stdout, and says on stderr whether they are the `built-in default` or `override: <path>`. `kota instructions > ~/.config/kota/instructions.md` gives you a starting point. `check` shows the source too.

## Your own agent settings

Keybindings, theme and similar preferences can be set per user, for every session in every project, with an agent override: a file under `$XDG_CONFIG_HOME/kota/` (`~/.config/kota/` by default) in the agent's own format. Without one, the bundled config is used as is. An override is read on the host when the session starts, merged over the bundled config (objects key by key, other values replaced), and never mounted, so the agent can't change it. Only the keys listed below are allowed. Anything else, invalid JSON, a file that isn't a regular file or one over 64 KiB stops `run`, `shell` and `check` with the file and key. `$schema` is always accepted and dropped. `run` and `check` print `agent override:` with the files in use, or `none (bundled config)`.

| File | Agent setting | Allowed keys |
|---|---|---|
| `opencode/cli.json` | opencode v2's terminal preferences (`~/.config/opencode/cli.json`) | `keybinds`, `leader`, `theme`, `animations`, `cursor`, `mouse`, `scroll`, `prompt`, `session`, `tabs`, `diffs`, `terminal`, `mini`, `highlights` |
| `opencode/opencode.json` | opencode's config (`~/.config/opencode/opencode.json`) | `compaction` |
| `pi/settings.json` | pi's settings (`~/.pi/agent/settings.json`) | `defaultThinkingLevel`, `modelThinkingLevels`, `thinkingBudgets`, `hideThinkingBlock`, `steeringMode`, `followUpMode`, `doubleEscapeAction`, `treeFilterMode`, `compaction`, `branchSummary`, `retry`, `theme`, `quietStartup`, `tuiMode`, `fullscreenExitOutput`, `fullscreenScrollbar`, `fullscreenCopyOnSelect`, `fullscreenWheelScrollLines`, `editorPaddingX`, `outputPad`, `autocompleteMaxVisible`, `showHardwareCursor`, `terminal`, `images`, `markdown` |
| `pi/keybindings.json` | pi's keybindings (`~/.pi/agent/keybindings.json`) | every action; each value must be a key or a list of keys (empty to disable the action) |

For example, to move opencode's session delete off `ctrl+d`:

```json
{
  "$schema": "https://opencode.ai/v2/cli.json",
  "keybinds": {
    "session.delete": "ctrl+shift+d"
  }
}
```

The rendered settings reach opencode through `OPENCODE_CLI_CONFIG_CONTENT`, which opencode merges over its global `cli.json` and keeps authoritative. opencode has no project-level `cli.json`, so the project can't change them. Settings you change in the TUI go to the session's `~/.config/opencode/cli.json` on its tmpfs and are lost after the session; put lasting ones in the override. `plugins` (loads code), `attention` (takes sound file paths; notifications can't leave the sandbox), `debug` and `experimental` are refused.

opencode's `opencode.json` is the bundled base, then your override, then the gateway provider, models and `--auto` permissions kota sets, so `share: disabled`, `autoupdate: false` and the permissions stay. Only `compaction` is allowed, in opencode 2's form (`auto`, `keep.tokens`, `buffer`) or v1's, which opencode maps. `keybinds` and `theme` are refused with a pointer to `cli.json`, where opencode 2 keeps them. Everything else is refused, under its v1 or v2 name, with the reason in the error: keys that run or load code (`plugin`, `mcp`, `lsp`, `formatter`, `command`, `agent`, `shell`), read paths (`instructions`, `watcher`, `skills`, `references`, `worktree`), set the gateway wiring or the approval model (`provider`, `model`, `small_model`, `enabled_providers`, `permission`, `tools`, `policies`, `websearch`), and `share`, `autoupdate`, `server` and `experimental`. `opencode debug config` in `kota shell` shows the config opencode reads.

pi's `settings.json` is the bundled base, then your override, then the provider and model kota sets, so `--model` still picks the model and the base's `defaultProjectTrust: never`, disabled telemetry and disabled llama.cpp extension stay. Refused, with the reason in the error: anything that loads files or code (`packages`, `extensions`, `skills`, `prompts`, `themes`, so `theme` names a built-in theme), runs commands (`shellPath`, `shellCommandPrefix`, `npmCommand`, `externalEditor`), changes trust, telemetry or the network route (`defaultProjectTrust`, `enableInstallTelemetry`, `enableAnalytics`, `httpProxy`, `transport`), or that kota owns (`defaultProvider`, `defaultModel`, `enabledModels`, `defaultTools`, `sessionDir`).

pi gets its `keybindings.json` rendered into its agent directory and mounted read-only, like `settings.json`; without an override it is empty and pi uses its defaults. pi has no project-level keybindings. `/hotkeys` in pi shows the bindings in effect, and `/reload` rereads the file, but edits to the override only reach the next session.

## Clearing pi's context between tickets

pi has a `ticket_done` tool (pi only; opencode relies on its own `/compact`). The agent calls it with the ticket and the commit once a ticket is done and committed. When that turn ends, the conversation is replaced by a short fixed note: the ticket is done in that commit, the agent's state is in the tickets and git history, so re-read `AGENTS.md` and the tracker, take the next ticket, and keep working unattended if you asked for that. None of the earlier conversation is kept, and the model writes no summary. pi then carries on with the next ticket in the same session. In the interactive UI the clear shows as a compaction, and the full transcript stays in the state volume.

pi's own compaction, which runs when the context fills up or on `/compact`, has the model summarize the old messages. With a small local model, that summary loses detail, so a clean cut at each ticket boundary works better. Automatic compaction and `/compact` are unchanged.

The tool isn't a gate. It doesn't check that the commit exists or that the ticket is marked done; the instructions say when to call it. A call without both arguments, or with a ticket path outside `/workspace`, returns an error and clears nothing.

The tool is a pi extension in `assets/agents/pi/extensions/ticket-done.ts`. kota mounts that directory read-only at `~/.pi/agent/extensions`, so the agent can't change the extension or add one beside it, and `check` verifies that. pi loads extensions from its agent directory without project trust; the project's `.pi/extensions` stay untrusted.
