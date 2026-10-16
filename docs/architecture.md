# Architecture

How kota is put together, as diagrams. The README describes each behaviour in detail; terms are as in `CONTEXT.md`, and the Engine VM decision is in `docs/adr/0001-engine-vm-for-agent-containers.md`.

## Runtime view

What runs where during a `run` or `shell` **Session** of a project with an **Engine VM**. Projects without an `engine:` section have no VM and no engine forwarder.

```mermaid
flowchart LR
    subgraph host["Host (macOS)"]
        cli["kota CLI"]
        gw["Gateway<br/>llama-swap :8686"]
        project[("Project dir<br/>~/Projects/app")]
        cache[("User cache dir<br/>kota/")]
        subgraph vm["Engine VM (QEMU)"]
            dockerd["dockerd<br/>TLS :2376"]
            ctrs["containers the agent starts<br/>(testcontainers, docker run)"]
        end
    end

    subgraph dvm["Docker VM (Rancher Desktop)"]
        subgraph internal["internal network: no route out"]
            sandbox["Sandbox container<br/>opencode / pi / bash<br/>your UID, all caps dropped"]
        end
        fwd["forwarder<br/>(socat)"]
        efwd["engine forwarder<br/>(socat)"]
        vols[("volumes:<br/>state, deps, shared caches")]
    end

    sandbox -- "gateway:8080" --> fwd -- "only the gateway port" --> gw
    sandbox -- "engine:2376,<br/>published ports 40000-40099" --> efwd -- "host 127.0.0.1<br/>QEMU hostfwd" --> dockerd
    dockerd --- ctrs
    project -- "bind /workspace<br/>secrets masked, .git hooks/config read-only" --> sandbox
    vols --> sandbox
    cli -- "docker compose" --> dvm
    cli -- "starts, loads images, stops" --> vm
    cache -. "archives, certs, base disk" .-> vm
```

Containment, enforced from outside the agent:

- **Sandbox:** attached only to an internal Docker network. Its one way out is the forwarder, which relays a single port to the **Gateway**.
- **Engine VM:** QEMU user-mode networking with `restrict=on`. The VM can't open connections out; only the Docker API and the published-port range are forwarded in, bound to the host's `127.0.0.1`.
- **Engine API:** needs a TLS client certificate made for this **Session**, because other containers can reach the host's loopback too.
- **Probes** check all of this before every **Session**. A failure blocks it.

## Lifecycle of a project

```mermaid
flowchart LR
    build["kota build<br/>base image, forwarder,<br/>Engine base disk"]
    pbuild["kota build dir<br/>project image from<br/>kota.Dockerfile"]
    prep["kota prep<br/>network on, no agent:<br/>dependencies, Engine images"]
    run["kota run / shell<br/>offline Session"]
    check["kota check<br/>mounts, models,<br/>Engine VM, probes"]
    clean["kota clean<br/>volumes, prep record,<br/>engine data"]

    build --> pbuild --> prep --> run
    build --> prep
    run -- "manifest or kota.yaml changed" --> prep
    prep -. "optional" .-> check -.-> run
    run --> clean
    clean -- "prep again" --> prep
```

`build` runs once per machine (and after updates). `prep` runs whenever dependencies or **Engine images** change, and asks you to type `y` when something that decides what it runs has changed since its last run. Only one command at a time can use a project. The session registry holds a per-project lease across all of them.

## `run` and `shell` startup

```mermaid
flowchart TD
    start(["kota run / shell"]) --> lease["Take the project lease<br/>(stop orphaned containers and Engine VM)"]
    lease --> validate["Validate agent, models.yaml,<br/>dependency plan"]
    validate --> auto{"run, full auto,<br/>uncommitted changes?"}
    auto -- "yes" --> ask["List changes, ask for y"]
    ask -- "no" --> abort(["abort"])
    ask -- "y" --> prepare
    auto -- "no" --> prepare["Prepare the session:<br/>mask plan and protections,<br/>image choice, session dir,<br/>Engine VM: config, archives,<br/>ports, certs, QEMU command"]
    prepare --> snap["Snapshot watched files"]
    snap --> vmstart["Start the Engine VM<br/>(boots while the sandbox starts)"]
    vmstart --> compose["docker compose up:<br/>forwarders + sandbox"]
    compose --> configure["Render agent config from<br/>the gateway's /v1/models"]
    configure --> ready["Wait for the Engine API,<br/>load image archives"]
    ready --> probes{"Sandbox probes,<br/>then Engine VM probes"}
    probes -- "a probe failed" --> refuse(["refuse to start"])
    probes -- "all passed" --> agent["Agent or Bash runs<br/>in /workspace"]
    agent --> teardown["Teardown: containers, Engine VM,<br/>placeholders, session dir"]
    refuse --> teardown
    teardown --> report["Report changed files<br/>that run things"]
    report --> release(["release the lease"])
```

The order from "Start the Engine VM" to the probes, and the teardown on any failure or signal, is in `internal/lifecycle`, shared by `run`, `shell` and `check`. Without an `engine:` section the Engine VM steps are skipped. With `--no-engine` they're skipped too, and the agent is told its container **Gates** can't run. Ctrl-C at any step leads to the same teardown.

## `prep`

```mermaid
flowchart TD
    start(["kota prep"]) --> lease["Take the project lease"]
    lease --> plan["Detect installs (package.json, go.mod,<br/>kota.yaml), read engine section"]
    plan --> images["Look up the tags recorded<br/>by the last prep (crane)"]
    images --> gate{"First prep, or inputs<br/>or images changed?"}
    gate -- "yes" --> ask["List commands, changed files,<br/>new / moved / removed images;<br/>ask for y"]
    ask -- "no" --> abort(["abort"])
    gate -- "no" --> vols
    ask -- "y" --> vols["Recreate dependency volumes"]
    vols --> fetch["Fetch phase: caches writable,<br/>no project code runs"]
    fetch --> eimg["Download new or moved Engine images<br/>by digest, into the archive cache"]
    eimg --> install["Install phase: caches read-only,<br/>lifecycle scripts and prep.command run"]
    install --> report["Report changed files<br/>that run things"]
    report --> record(["Save the prep record"])
```

## Packages

`cmd/kota` wires the commands together. The packages under `internal/` are mostly pure and table-tested. `docker` and `enginevm` are the thin adapters over the Docker CLI and QEMU.

```mermaid
flowchart TD
    main["cmd/kota<br/>commands, session wiring"]

    subgraph policy["What may be mounted and shown"]
        pathpolicy["pathpolicy<br/>allowed project and --ro paths"]
        mask["mask<br/>secret masks, git and .envrc protections"]
        deps["deps<br/>dependency plan, kota.yaml prep"]
        projectimage["projectimage<br/>kota.Dockerfile"]
        nohistory["nohistory<br/>No-history copy: inspect, clone,<br/>kota config from the original"]
    end

    subgraph spec["Sandbox definition"]
        sandbox["sandbox<br/>compose spec, names, gateway"]
        models["models<br/>catalog from gateway + models.yaml"]
        opencode["opencode"]
        pi["pi"]
        gatewaykey["gatewaykey"]
    end

    subgraph verify["Verification and reports"]
        probe["probe<br/>probe definitions"]
        check["check<br/>run probes"]
        changereport["changereport<br/>watched files"]
        prepgate["prepgate<br/>prep confirmation"]
        approval["approval<br/>uncommitted changes"]
    end

    subgraph eng["Engine VM"]
        enginesession["enginesession<br/>a session's Engine VM:<br/>prepare, start, ready, probes, stop"]
        engineimages["engineimages<br/>a project's image archive cache:<br/>plan, fetch with crane"]
        engine["engine<br/>config, QEMU command, certs,<br/>image plan, base disk"]
        enginevm["enginevm<br/>QEMU adapter"]
    end

    subgraph runtime["Runtime"]
        lifecycle["lifecycle<br/>start and stop order,<br/>teardown on failure or signal"]
        docker["docker<br/>Docker CLI adapter"]
        sessionregistry["sessionregistry<br/>per-project lease"]
        hostdata["hostdata<br/>kota's directory on the host"]
        interrupt["interrupt<br/>signals"]
    end

    main --> policy & spec & verify & eng & runtime
    mask --> deps & projectimage & sandbox
    pathpolicy --> sandbox
    nohistory --> approval & mask & deps & projectimage
    opencode & pi --> models
    check --> docker & probe & sandbox
    probe --> sandbox
    prepgate --> changereport & deps
    docker --> sandbox & interrupt
    enginevm --> engine & interrupt
    enginesession --> engine & enginevm & check & docker
    lifecycle --> interrupt
    engineimages --> engine
    hostdata --> engine & sandbox
```

## Where data lives

Everything under `kota/` in the user cache dir comes from `internal/hostdata`, which also lists what `clean` removes for a project.

| What | Where | Removed by |
| --- | --- | --- |
| Project source | the project dir, mounted at `/workspace` | never |
| Agent state | volume `kota-state-<agent>-<hash>` | `clean` |
| Dependency dirs | volumes `kota-deps-<hash>-<hash>` | `prep` (recreated), `clean` |
| Shared caches (Go modules, corepack, pnpm store) | volumes `kota-cache-*` | `clean --shared-caches` |
| Session lease records | `kota/registry/` in the user cache dir | kept (lock files) |
| Prep confirmation record | `kota/prep/` | `clean` |
| Session dir (agent config, engine certs, console log) | `kota/sessions/` | end of session, `clean` for leftovers |
| No-history copies | `kota/copies/` | `clean` |
| Engine base disk, kernel, initrd | `kota/engine/base/` | replaced by `build` |
| Engine image archives, VM pidfile | `kota/engine/projects/<hash>/` | `clean` |
