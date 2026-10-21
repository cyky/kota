# PRD — Named gateways and models in kota config

Status: ready-for-agent
Category: enhancement

## Problem Statement

kota knows one **Gateway** at a time: a URL from `--gateway` or `KOTA_GATEWAY`, with its key from `KOTA_GATEWAY_KEY` or `KOTA_GATEWAY_KEY_FILE`, and the models built into the binary. A user who works with more than one gateway, such as llama-swap for the models that fit the machine and a gateway in front of heavier models on servers, has to retype URLs and swap exported keys by hand, and a key exported for one gateway is sent to whichever gateway the next command uses. The default model, the small model and each model's context window can only be changed by editing the built-in models file and reinstalling kota, and they are the same for every gateway, though different gateways serve entirely different models.

## Solution

The user defines named **Gateways** in one file in the kota config dir, `~/.config/kota/config.yaml`. Each gateway has a URL, optionally a key file, and optionally its own models. The file can also set default models for gateways without their own, and a default gateway. Every command picks one gateway: `--gateway work-api`, `KOTA_GATEWAY=work-api`, or the config's default, and a URL still works as before. A project never chooses its gateway or model: `kota.yaml` is unchanged. `kota check` says which gateway, key and models were chosen and why, and `run` and `shell` say which gateway they use when they start. Without a config file, kota behaves exactly as today.

```yaml
defaultGateway: local
models:                  # config defaults, for gateways without their own
  default: qwen3.8-27b
  unknown: { context: 8192, output: 2048, tools: true }
  models:
    qwen3.8-27b: { context: 131072, output: 16384, tools: true }
gateways:
  local:
    url: http://host.docker.internal:8686
  work-api:
    url: http://host.docker.internal:4000
    keyFile: ~/.config/kota/keys/work-api
    models:
      default: big-coder
      unknown: { context: 32768, output: 8192, tools: true }
      models:
        big-coder: { context: 200000, output: 32000, tools: true }
```

## User Stories

1. As a user with several gateways, I want to define each one once under a name, so that I don't retype URLs.
2. As a user, I want to pick a gateway by name with `--gateway NAME`, so that switching between a local and a remote gateway is one flag.
3. As a user, I want `KOTA_GATEWAY` to accept a gateway name too, so that a shell alias or direnv can choose one.
4. As a user, I want a `defaultGateway` in my config, so that plain `kota run` uses the gateway I use most.
5. As a user, I want `--gateway http://...` to keep working, so that I can try a gateway without adding it to the config.
6. As a user, I want `--gateway` to win over `KOTA_GATEWAY`, and `KOTA_GATEWAY` over `defaultGateway`, so that the most specific choice wins.
7. As a user without a config file, I want kota to behave exactly as before, so that nothing breaks when I upgrade.
8. As a user, I want a mistyped gateway name to fail with the list of names I've defined, so that I can see the right one.
9. As a user, I want each gateway to have its own key file, so that each key is sent only to the gateway it belongs to.
10. As a user, I want `KOTA_GATEWAY_KEY` and `KOTA_GATEWAY_KEY_FILE` to be ignored for a named gateway, so that a key exported in my shell never reaches a gateway it wasn't meant for.
11. As a user giving a gateway as a URL, I want the key environment variables to work as before, so that my current setup keeps working.
12. As a user, I want the config to hold a path to the key, never the key itself, so that the config can live in my dotfiles repo without leaking keys.
13. As a user, I want a key file read with the same rules as `KOTA_GATEWAY_KEY_FILE` (trimmed, non-empty, printable ASCII without spaces), so that a bad key can't break the request it's sent in.
14. As a user, I want errors about a key never to quote it, so that a key doesn't end up in my terminal history or a bug report.
15. As a user, I want only the chosen gateway's key file read, so that a missing work key doesn't stop a local Session.
16. As a user, I want to set a gateway's own models (default, small model, context sizes, tool support, hidden models), so that each gateway gets the models it actually serves.
17. As a user, I want to set default models in my config, so that I can change the default model or a context size without reinstalling kota.
18. As a user, I want a gateway with its own models to use only those, with nothing merged in from my config defaults or the built-in models, so that a remote gateway's heavy models and a local gateway's small ones never mix.
19. As a user, I want a gateway without its own models to use my config defaults, or the built-in models when I have none, so that I don't have to repeat models for every gateway.
20. As a user giving a gateway as a URL, I want my config defaults to apply, so that my default model holds for one-off gateways too.
21. As a user, I want each models section to have exactly the format of kota's built-in models file, with the same checks, so that there is one format to learn and the built-in file is a starting point.
22. As a user, I want an incomplete models section (no default, or an incomplete `unknown`) to be an error that names the gateway, so that I find the mistake right away.
23. As a user, I want unknown keys in the config to be errors with their line number, so that a typo doesn't silently fall back to defaults.
24. As a user, I want the whole config checked whenever it's read, not only the chosen gateway, so that a broken entry shows up before the day I need it.
25. As a user, I want a key file path that isn't absolute or `~/`-relative to be an error, so that the file kota reads is never ambiguous.
26. As a user, I want `build`, `prep`, `clean` and `instructions` not to read the config, so that a typo in it never blocks a build or a cleanup.
27. As a user, I want `kota check` to show the gateway's name, URL and where the choice came from, so that I can tell why a gateway was picked.
28. As a user, I want `kota check` to show where the key came from, never its value, so that I can tell which key was used without exposing it.
29. As a user, I want `kota check` to show which models apply (the gateway's own, config defaults, built-in), so that a surprising default model is easy to trace.
30. As a user, I want `run` and `shell` to print the gateway they use when they start, so that I notice when I'm on the remote one.
31. As a user running two Sessions at once, I want the model-clash warning only when they use the same gateway, so that a local and a remote Session don't warn for nothing.
32. As a user, I want the clash warning to stay a warning, so that it never stops a Session.
33. As a user of a gateway that isn't llama-swap, I want the clash warning's wording not to assume llama-swap, so that it reads as advice, not as an error.
34. As a project maintainer, I want `kota.yaml` unable to choose a gateway, so that cloning a repo can never send my prompts to someone else's server.
35. As a project maintainer, I want `kota.yaml` unable to choose a model, so that a shared repo doesn't break on a colleague's gateway with different model names.
36. As a user, I want `--model ID` to keep working with any gateway, so that I can pick a model for one Session.
37. As a user, I want the docs and `kota help` to describe named gateways, keys and models, so that I can set them up without reading the code.
38. As a user who sets `XDG_CONFIG_HOME`, I want the config read from `$XDG_CONFIG_HOME/kota/config.yaml`, so that it sits next to my agent overrides and instructions.

## Implementation Decisions

- **A new deep module resolves the gateway for one command.** Its one entry point takes the user's config dir, the raw `--gateway` value (already defaulted from `KOTA_GATEWAY`) and the environment, and returns a choice: the gateway's name (empty for a URL), the gateway address, the key, and the models file, each with the source to display. It reads and checks the whole `config.yaml`, applies the order (`--gateway`, `KOTA_GATEWAY`, `defaultGateway`, the built-in `http://host.docker.internal:8686`), reads only the chosen gateway's key file, and picks one models set: the gateway's own, otherwise the config's defaults, otherwise the built-in models. Nothing is merged between the sets.
- **The key loading moves into that module.** Today's environment-variable key loading and its checks become part of it, and apply unchanged to `keyFile`. For a named gateway only its `keyFile` is used; for a URL gateway only the environment variables.
- **A value is a URL if it starts with `http://`**, parsed by the existing gateway URL parser (plain http, a host, no path). Anything else is a gateway name.
- **The config file is parsed strictly.** Unknown keys are errors with their line. Top-level keys are `defaultGateway`, `models` and `gateways`. Each gateway has `url`, and optionally `keyFile` and `models`. A missing config file is not an error and means today's behaviour. Key file paths must be absolute or start with `~/`.
- **The models package gains parsing of a models section embedded in another file**, with the same checks as the built-in models file (`default` set, `unknown` complete, positive sizes, unknown keys rejected), and errors that say which section is wrong (the config's defaults, or a named gateway's). Matching the catalog against the gateway's served models is unchanged.
- **Session setup uses the chosen models** where it now always parses the built-in ones. The startup check of the built-in models file stays.
- **The session registry records each Session's gateway URL.** A model clash is reported only between Sessions on the same URL, and is still only a warning. Its wording becomes "a gateway that loads one model at a time (such as llama-swap) will swap models back and forth".
- **Only `run`, `shell` and `check` resolve a gateway.** `prep` has no gateway, as today.
- **`kota check` prints three lines** with their sources: gateway (name, URL, source), key (`keyFile <path>`, `KOTA_GATEWAY_KEY`, `KOTA_GATEWAY_KEY_FILE <path>`, or none; never the value), and models (the gateway's own, config defaults, built-in). `run` and `shell` print the gateway line once at startup.
- **`kota.yaml` is unchanged**: it keeps only `prep` and `engine`, and its strict parsing still rejects any `gateway` or `model` key ([ADR 0002](../../docs/adr/0002-gateways-defined-in-user-config.md)).
- **Docs**: the Gateway, key and Models sections of the sessions page, the README's settings table and Models paragraph, `kota help` for `--gateway`, and the architecture page's packages diagram for the new module and what it imports.

## Testing Decisions

- Tests check behaviour through each module's public interface: which gateway, key and models a given config and environment produce, and the errors a bad config gives. Not how the file is parsed.
- **The new resolver module** gets thorough unit tests on a temporary config dir with a fake environment: each step of the precedence order, a URL versus a name, an unknown name listing the defined ones, no config file, the three models sets with no merging between them, a named gateway ignoring the key environment variables, a URL gateway using them, only the chosen gateway's key file being read, whole-file validation (unknown keys with lines, a bad URL, an incomplete models section, an undefined `defaultGateway`, a relative key path), and no error ever containing a key. Prior art: the existing tests of the gateway key loading and of the models file.
- **The models package**: unit tests for an embedded section's checks and its error messages, extending the existing models file tests.
- **The session registry**: a unit test that Sessions on different gateway URLs don't clash and Sessions on the same one do, extending the existing clash test.
- **`kota check`**: one test of the three new output lines with a fake config, in the style of the existing command tests with a fake HOME (never the real one).
- **No new integration test**: the gateway route through the forwarder doesn't change.

## Out of Scope

- Gateways that forward to remote or cloud providers, and what the docs say about that route out of the **Sandbox** (issue 34 in `agent-sandbox`).
- How a gateway's key is created (LiteLLM virtual keys and similar).
- Choosing a gateway by project directory.
- Any gateway or model setting in `kota.yaml`.
- Environment variables for the **Session** from kota config; `ENV` in `kota.Dockerfile` covers settings.
- A command that prints the built-in models file as a starting point.

## Further Notes

- Design notes and the reasons behind each decision: [design.md](design.md). The boundary decision: [ADR 0002](../../docs/adr/0002-gateways-defined-in-user-config.md).
- `CONTEXT.md` already defines **Gateway** for named gateways, ahead of the implementation.
