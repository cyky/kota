# Named gateways and models in kota config

Design settled in a grilling session (2026-10-21), and implemented in issues 01-04 the same day.

## Where they are defined

- Gateways are defined by the user, each under a name, in one file: `config.yaml` in the kota config dir (`~/.config/kota/config.yaml`, or `$XDG_CONFIG_HOME/kota/config.yaml`). A gateway is never defined in a project file: a committed `kota.yaml` that held a URL would decide where a project's code is sent, for everyone who runs it.
- `kota.yaml` has no gateway or model setting; it keeps only `prep` and `engine`. Gateway names are the user's own, the same project switches gateways by situation, and model names mean something only on the gateway that serves them. `--model ID` covers a one-off choice, and a gateway's own `default` covers a model per gateway.
- No rules pick a gateway by project directory. The same project may use a local gateway one day and a remote one the next, so the choice is made per command.

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

## Choosing a gateway

- `--gateway` and `KOTA_GATEWAY` take a name from the config or an `http://` URL, which is used as given, as today. Order, highest first: `--gateway`, `KOTA_GATEWAY`, the config's `defaultGateway`, the built-in `http://host.docker.internal:8686`.
- An unknown name is an error that lists the defined names.
- Without a config file, kota behaves as today.

## Models

Each gateway can set its own models (the default and small model, context sizes and similar), so changing them no longer means reinstalling kota. The config can also set default models at its top level. One whole set applies, the first that exists:

1. the gateway's own models;
2. the config's default models (also for a URL gateway);
3. the models built into kota (`assets/models.yaml`), as today.

Nothing is merged between the sets: a gateway that sets models gets nothing from the config's defaults, the built-in models or another gateway, and the config's defaults get nothing from the built-in models. Gateways can serve entirely different models, such as heavier ones on servers behind a remote gateway and only what fits the machine behind a local one.

Each models section has exactly the format of `assets/models.yaml`, with the same checks: `default` and a complete `unknown` are required, and unknown keys are errors. The top-level key for the gateway is `defaultGateway`, so it doesn't read like `models.default`.

## Keys

- A gateway's key comes from its optional `keyFile:`, a path to a file that holds the key, never the key itself in the config: the kota config dir often ends up in a dotfiles repo.
- For a named gateway, only its own `keyFile` is used, and `KOTA_GATEWAY_KEY` and `KOTA_GATEWAY_KEY_FILE` are ignored, so each key goes to one gateway only. For a URL gateway, the environment variables work as today.
- How a key is created is up to the gateway and out of scope.

## Validation

- `keyFile:` is read as `KOTA_GATEWAY_KEY_FILE` is today: trimmed, an empty file is an error, the key must be printable ASCII without spaces, and errors never quote it. Errors name the gateway.
- Key file paths are absolute or start with `~/`; a relative path is an error. Where the file lives isn't enforced: the docs say to keep it outside projects, as today.
- Only `run`, `shell` and `check` read `config.yaml`, so a typo in it can't block `build`, `prep`, `clean` or `instructions`.
- Those commands check the whole file, not only the chosen gateway: unknown keys are errors with their line, every `url` must parse, every models section must be complete, and `defaultGateway` must name a defined gateway. Only the chosen gateway's key file is read.

## What kota shows

- `kota check` gives the gateway's name, URL and source (`--gateway`, `KOTA_GATEWAY`, `defaultGateway`, built-in default), the key's source (`keyFile <path>`, `KOTA_GATEWAY_KEY`, `KOTA_GATEWAY_KEY_FILE <path>`, none), never its value, and the models' source (the gateway's own, config defaults, built-in).
- `run` and `shell` print the gateway line once at startup.
- The model-clash warning stays a warning and compares only Sessions on the same gateway URL, which the session registry now records. Its wording no longer assumes llama-swap: "a gateway that loads one model at a time (such as llama-swap) will swap models back and forth". There is no per-gateway setting for it: the agent can switch models during a Session anyway.

## Not decided here

- Gateways that forward to remote providers, and what the docs say about them (issue 34).
