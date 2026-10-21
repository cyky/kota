# 01 — Named gateways, picked by name

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/gateway-config/PRD.md`

## What to build

The tracer bullet for named **Gateways**. The user defines gateways in `config.yaml` in the kota config dir (`$XDG_CONFIG_HOME/kota/`, default `~/.config/kota/`), each under a name with a `url`, and optionally a `defaultGateway`. `--gateway` and `KOTA_GATEWAY` take a gateway name or an `http://` URL. The order, highest first: `--gateway`, `KOTA_GATEWAY`, `defaultGateway`, the built-in `http://host.docker.internal:8686`.

This introduces the module that resolves the gateway for one command, with one entry point that returns the chosen gateway, its name (empty for a URL) and the source of the choice. Slices 02 and 03 extend that module with keys and models. Here, keys and models work exactly as today: the key from `KOTA_GATEWAY_KEY` or `KOTA_GATEWAY_KEY_FILE`, the built-in models.

Only `run`, `shell` and `check` read `config.yaml`. They parse the whole file strictly: unknown keys are errors with their line, every `url` must parse as today's gateway URLs do, and `defaultGateway` must name a defined gateway. Without the file, kota behaves exactly as today. In this slice, a `keyFile` or `models` key under a gateway, or a top-level `models`, is still an unknown key; slices 02 and 03 add them.

`kota check` shows the gateway's name, URL and source (`--gateway`, `KOTA_GATEWAY`, `defaultGateway` or the built-in default). `run` and `shell` print the same line once at startup. `kota.yaml` is unchanged ([ADR 0002](../../../docs/adr/0002-gateways-defined-in-user-config.md)).

## Acceptance criteria

- [ ] `--gateway NAME` and `KOTA_GATEWAY=NAME` use the named gateway's URL; an `http://` URL is used as given, as today
- [ ] The order is `--gateway`, `KOTA_GATEWAY`, `defaultGateway`, the built-in default
- [ ] An unknown name fails with the list of defined names
- [ ] Without `config.yaml`, behaviour and output are unchanged
- [ ] Unknown keys, a bad `url` and an undefined `defaultGateway` are errors naming the file and line or gateway, wherever they are in the file
- [ ] `build`, `prep`, `clean` and `instructions` don't read `config.yaml`: a broken one doesn't affect them
- [ ] `kota check` shows the gateway's name, URL and source; `run` and `shell` print it at startup
- [ ] Unit tests for the resolver cover every step of the order, a URL versus a name, an unknown name, no config, and each validation error, using a temporary config dir and a fake environment (never the real home)
- [ ] `docs/sessions.md`, the README's settings table, `kota help` for `--gateway`, and `docs/architecture.md` (the new package and its imports) describe named gateways

## Blocked by

None - can start immediately
