# DONE — 01 — A named gateway can run on an existing Docker network

Status: done
Category: enhancement
Resolved in: fb2a8ed

## What to build

Today the gateway forwarder reaches the **Gateway** through the session's own `egress` network, so a **Gateway** that runs in a container elsewhere (another compose project, say) has to publish its port on the host and be configured as `http://host.docker.internal:<port>`. Let a named gateway instead name the Docker network it runs on, so its URL can use the container's hostname and no port has to be published:

```yaml
gateways:
  team:
    url: http://model-server:4000
    dockerNetwork: models_internal
```

- **Only named gateways.** `dockerNetwork` is a key of a gateway in `~/.config/kota/config.yaml`. There is no flag or environment variable, and a gateway given as a URL (`--gateway URL`, `KOTA_GATEWAY`) can't have one, like keys. `kota.yaml` can't set it (ADR 0002): a project must not choose where the forwarder is attached.
- **The value** is the network's Docker name as `docker network ls` shows it, compose prefix included. kota never creates or removes it: in the compose spec it is an `external` network with that name. A network that doesn't exist stops `run`, `shell` and `check` with the config file, the gateway and the network.
- **Refused values.** `host`, `none`, `bridge`, any name starting with `kota-` (kota's own session networks, which would connect this forwarder to another session's sandbox) and anything that isn't a valid Docker network name stop `run`, `shell` and `check` with the config file, the gateway and the value. `host` can't be combined with the internal network and would give the forwarder the host's network stack; `none` has no route; the default `bridge` has no DNS, so a container hostname wouldn't resolve, and holds every container started without a network.
- **Only the gateway forwarder joins it, instead of `egress`.** The forwarder is on the internal `sandbox` network (alias `gateway`, as now) and on the named network, and nothing else, so its only routes lead to that network's containers. The agent container stays on `sandbox` only. The engine forwarder is unchanged and stays on `egress`. Without `dockerNetwork`, the spec is unchanged.
- **Prep is unaffected:** it has no forwarder and no gateway.
- **Probes.** No new probe. The gateway host is resolved from the forwarder, as now, so the gateway container's IP on its network, at the gateway's port and the usual ports, is already a must-fail target from the agent. `Session.NetworkGateways` looks networks up by their compose names; it must look up an external network by its own name, and add its bridge gateway address as a must-fail target unless the network is internal. Resolving `host.docker.internal` from the forwarder may fail on such a network; that only shrinks the target list, as discovery failures already do.
- **Showing it.** `check` shows the network next to the gateway, for example `team http://model-server:4000 on Docker network models_internal (defaultGateway)`; `run` and `shell` print it in their gateway line too.

The model list (`/v1/models`) and the gateway probe already go through the forwarder, so they work with a container hostname unchanged.

## Docs

- `docs/sessions.md`: the Gateway section gets `dockerNetwork`, its refused values and an example; the Network section says the forwarder joins the gateway's network instead of `egress` when one is named.
- `docs/sandbox.md`: the forwarder paragraph under the network description.
- `docs/architecture.md`: the runtime diagram and its bullets show the forwarder on the gateway's network as the alternative to `egress`.
- `README.md`: the gateway prerequisite and `KOTA_GATEWAY` row no longer imply the gateway is always on the host.
- `CONTEXT.md` already states it (Gateway, and its relationship line).

## Acceptance criteria

- [x] A named gateway with `dockerNetwork` attaches the gateway forwarder to that external network and `sandbox`, not to `egress`; the agent stays on `sandbox` only, and the engine forwarder is unchanged
- [x] A container hostname in such a gateway's URL works for the model list, the gateway probe and the agent
- [x] Without `dockerNetwork`, the compose spec is unchanged
- [x] `dockerNetwork` on a gateway given as a URL isn't possible; there is no flag or environment variable for it, and `kota.yaml` has no such key
- [x] A missing network, `host`, `none`, `bridge`, a `kota-` name or an invalid name stops `run`, `shell` and `check` with the config file, the gateway and the value
- [x] `NetworkGateways` finds the external network by its name and adds its gateway address unless it is internal; the containment probes pass with a gateway on a Docker network
- [x] `check`, `run` and `shell` show the gateway's Docker network
- [x] The docs listed above describe it

## Comments

### 2026-10-23

Verified: unit tests for the spec (forwarder on `sandbox` and the external network only, engine forwarder keeps `egress`, `egress` declared only when used, `Addr` tells networks apart), for the config (`dockerNetwork` parsed, each refused value, the gateway line), and integration tests against Docker: a stub gateway container on its own network serves `/v1/models` through the forwarder and every containment probe passes, including a direct probe of the gateway's port; with the agent attached to that network the probes fail. `TestContainedSandboxPassesAllProbes` still passes. The other integration tests weren't run.

One difference: `NetworkGateways` adds an external network's gateway address always, not only when it isn't internal. An isolated internal network has none, and on any other it is a Docker VM address the agent must not reach, so this is stricter.

Not verified: a real `kota run` or `kota check` against a `config.yaml` with `dockerNetwork`; the missing-network error in `main` has no test.

### 2026-10-23 (review)

Review fixes, in the same commit: the missing-network check is `Choice.CheckNetwork`, with the config path unexported and a unit test (no network, existing, missing, lookup failure); the runtime diagram in `docs/architecture.md` draws the gateway container on its own network in the Docker VM, with the forwarder's alternative route to it; the refusal rules are `sandbox.CheckGatewayNetwork`, sharing the `kota-` prefix with `ProjectName`; the hint is added once in `gatewayconfig`, and not to an invalid name; `NetworkGateways` gets each network's Docker name from `Compose.NetworkName`; and the README `KOTA_GATEWAY` row names a gateway on a Docker network and says the default is on the host.
