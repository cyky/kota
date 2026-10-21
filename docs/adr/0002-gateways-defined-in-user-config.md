---
status: accepted
---

# Gateways are defined in the user's config, never in a project

The **Gateway** is where a **Session** sends the agent's prompts and whatever of the project they carry, so the user chooses it, not the project. Named **Gateways**, with their keys and models, are defined only in the user's `~/.config/kota/config.yaml` and picked per command (`--gateway NAME`, `KOTA_GATEWAY`, or the config's `defaultGateway`). `kota.yaml` has no gateway or model setting. This keeps a committed project file from deciding, for everyone who runs it, where the project's code goes.

## Considered Options

- **A gateway URL in `kota.yaml`.** Rejected: cloning a repo whose `kota.yaml` names someone else's server would send that server the Session's prompts on the first `kota run`, without a prompt. A convention of committing only a `kota.yaml.example` would help in the user's own repos, but kota can't enforce it in others'.
- **`kota.yaml` picks one of the user's gateways by name.** Rejected: gateway names are personal, so a name in a shared repo breaks for a colleague who named theirs differently, and the same project is run against a local gateway one day and a remote one the next.
- **Directory rules in the user's config** (everything under `~/Projects/work/` uses `work`). Rejected for the same reason: the choice depends on the situation, not on the project.
- **A model setting in `kota.yaml`.** Rejected: a model name means something only on the gateway that serves it.

## Consequences

- A project can't ask for a gateway or a model. `--gateway` and `--model` are the per-command choice; a gateway's own `default` model is the per-gateway one.
- Each named gateway's key comes only from its own `keyFile`, so a key exported in the shell is never sent to a gateway it wasn't meant for. The `KOTA_GATEWAY_KEY` variables apply only to a gateway given as a URL.
- Without a config file, kota behaves as before: llama-swap at `http://host.docker.internal:8686` and the built-in models.
