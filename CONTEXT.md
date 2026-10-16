# kota

Runs coding agents against local models in an offline sandbox, scoped to one project, so the agent can work unattended without access to the rest of the machine.

## Language

**Session**:
One agent (or shell) run in the **Sandbox** for one project, started by `kota run` or `kota shell`.
_Avoid_: container, run (as a noun)

**Sandbox**:
The offline container environment a **Session** runs in: the project mounted, secrets masked, no route out except the **Gateway**.
_Avoid_: jail, VM

**Gateway**:
The local model server (llama-swap) on the host, the only network destination reachable from the **Sandbox**.
_Avoid_: proxy, LLM server

**Prep**:
The separate, network-enabled install of a project's dependencies, run without an agent before **Sessions** need them.
_Avoid_: setup, install step

**Gate**:
A project check (tests, lint, build) that the agent runs itself inside the **Session** to verify its work before calling a task done, without the user's input.
_Avoid_: CI, verification step, quality check

**Engine VM**:
A dedicated QEMU VM, managed by kota, whose container engine runs the containers the agent starts during a **Session** (for example testcontainers), apart from the host's Docker.
_Avoid_: Docker-in-Docker, sidecar engine, host Docker, Lima VM

**Engine base disk**:
The clean disk every **Engine VM** boots from, built once online by kota and never changed by a **Session**.
_Avoid_: engine image (that's an **Engine image**), template

**Engine image**:
A container image a project declares in `kota.yaml` for its **Engine VM**, downloaded during **Prep** outside the **Engine VM** and loaded into it, because the **Engine VM** never has a route out.
_Avoid_: test image, service image

**Change report**:
What kota prints after a command that ran project code in a container (`run`, `shell`, `prep`): watched files the command changed in the working tree, and branches, tags and commits it created or changed, with warnings for commits that touch masked, placeholder or globally ignored paths, or hide changes to watched files.
_Avoid_: diff, summary, session report

## Relationships

- A **Session** runs in exactly one **Sandbox**, for exactly one project
- A **Gate** runs inside a **Session**, never on the host
- **Prep** runs before **Sessions** and is the only step with network access to registries
- A **Gate** that needs containers (for example testcontainers) starts them in the **Engine VM**, never in the host's Docker
- Every **Engine VM** boots from the same **Engine base disk**, through a throwaway layer
- An **Engine VM** belongs to exactly one project. It runs only while a **Session** of that project does, and starts every **Session** from a clean disk with its **Engine images** loaded, so nothing carries over between **Sessions**
- An **Engine VM** has the same network containment as the **Sandbox**: it reaches only its own project's **Sandbox**, never has a route out (not even during **Prep**), is enforced from outside the VM, and checked by probes before every **Session**
- A project has an **Engine VM** only if it declares **Engine images**; other projects' **Sessions** never start one
- If a project's **Engine VM** can't start or fails a probe, its **Session** refuses to start, unless the user explicitly starts it without the engine. The agent is then told that container **Gates** can't run
- A **Gate** can only use **Engine images** the project declares; an undeclared image fails in the **Session** instead of being downloaded
- A **Change report** never runs git on the host. Commits are read in an offline container

## Example dialogue

> **Dev:** "Can the agent mark the task done after its unit tests pass?"
> **Domain expert:** "Only if those are all the project's **Gates**. If the integration tests are a **Gate**, the agent has to run them in the **Session** too, without me."
> **Dev:** "They use testcontainers with Postgres. Does the agent get Docker for that?"
> **Domain expert:** "Not the host's Docker. The project declares `postgres:16-alpine` as an **Engine image**, **Prep** downloads it, and the tests start it in the project's **Engine VM**, which can't reach anything but the **Sandbox**."
> **Dev:** "And if the **Engine VM** won't boot?"
> **Domain expert:** "Then the **Session** doesn't start, unless I start it without the engine on purpose. Then the agent has to report the container **Gates** as not run, not as passed."

## Flagged ambiguities

- "gate" could have meant a check kota runs after the **Session**, before the user accepts the changes. Resolved: a **Gate** is run by the agent inside the **Session**.
- The **Engine VM** was first planned as a Lima VM. Resolved: it is a QEMU VM, because Lima can't lock its network down from outside the VM.
- "a separate container that serves Docker" was considered for the agent's containers. Resolved: they run in the **Engine VM**, because a Docker-serving container would have to be privileged on the same VM as the host's Docker.
