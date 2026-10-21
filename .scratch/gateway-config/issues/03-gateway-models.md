# 03 — Models per gateway and config defaults

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/gateway-config/PRD.md`

## What to build

A named **Gateway** can set its own `models:`, and `config.yaml` can set default `models:` at its top level. Each section has exactly the format of kota's built-in models file, with the same checks: `default` set, `unknown` with `context`, `output` and `tools`, positive sizes, unknown keys rejected. Errors say which section is wrong (the config's defaults, or a named gateway's).

One whole set applies, the first that exists:

1. the chosen gateway's own models;
2. the config's default models (also for a gateway given as a URL);
3. the built-in models, as today.

Nothing is merged between the sets. A gateway with its own models gets nothing from the config's defaults, the built-in models or another gateway, and the config's defaults get nothing from the built-in models.

The resolver module from slice 01 returns the models set and its source; Session setup uses it where it now always parses the built-in models. The models package gains parsing of a section embedded in another file; matching the catalog against what the gateway serves is unchanged, and so is `--model`. `kota check` shows the models' source (the gateway's own, config defaults, built-in).

## Acceptance criteria

- [ ] A gateway's own models decide the default and small model and the model limits for its Sessions
- [ ] A gateway without its own models uses the config's defaults, and the built-in models without them; so does a URL gateway
- [ ] No field is merged between sets: a section that leaves something out is incomplete, not filled in from another set
- [ ] An incomplete section or an unknown key in one is an error naming the section, wherever it is in the file
- [ ] `--model ID` works as today with any gateway
- [ ] `kota check` shows the models' source
- [ ] Unit tests cover the three sets, the no-merge rule and each validation error; the models package's tests cover an embedded section
- [ ] `docs/sessions.md` and the README's Models paragraph describe models in `config.yaml`, so changing a default no longer means reinstalling kota

## Blocked by

- `.scratch/gateway-config/issues/01-named-gateways.md`
