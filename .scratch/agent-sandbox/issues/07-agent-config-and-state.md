# DONE — 07 — Agent config from repo and per-project state volumes

Status: done
Resolved in: `ef17262`

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

- opencode base config (permissions, defaults) versioned in this repo and mounted read-only into the sandbox.
- Agent state (sessions, logs, caches) stored in a Docker volume per agent per project, named from a hash of the resolved project path.
- Host `~/.config/opencode` (or any host agent config) is never mounted.
- Sessions can be resumed in a later `run` of the same project.

## Acceptance criteria

- [x] The agent can't modify its config inside the sandbox
- [x] A session started in project A can be resumed in a later `run` of project A
- [x] Project B can't see project A's sessions
- [x] Volume names are stable for the same path and differ across paths (unit test on SandboxSpec)
- [x] No host agent config directory appears in the mounts (unit test)

## Blocked by

- `01-tracer-bullet-offline-opencode-run.md`

## Comments

**2026-10-14 — Done.** Implemented in `ef17262` on `master`.

- Base config: `assets/agents/opencode/opencode.json` (edit and bash ask, webfetch deny, autoupdate off, share disabled), embedded as `assets.Agents`. `opencode.Render(base, settings)` adds the gateway provider and model and replaces any providers in the base. Mounted read-only at `/etc/localllm/opencode.json`.
- State: named volume `localllm-state-opencode-<32 hex of sha256(resolved path)>` at `/var/lib/localllm/state`, with `XDG_DATA_HOME`, `XDG_STATE_HOME` and `XDG_CACHE_HOME` inside it. The base image creates the directory mode 1777, which Docker copies into a new volume.
- Probes: "state writable" (must succeed), and the agent config is in the read-only list.

Findings in opencode 2.0.25:
- The project's `opencode.json` and `.opencode/` load after `OPENCODE_CONFIG` and override it, so the agent could have granted itself permissions for the next session. `OPENCODE_DISABLE_PROJECT_CONFIG=1` drops both (checked with `opencode debug config` and `debug agents`). A project's own opencode config is therefore not used in the sandbox.
- A read-only `OPENCODE_CONFIG_DIR` crashes opencode: its background service writes `service.json` there. `~/.config/opencode` stays on the per-session tmpfs instead. It loads before our config and is discarded after the session.

Verified:
- Unit tests: rendered config keeps ask permissions and drops base providers; volume names stable per path and different across paths and agents; volume mount and XDG env in the spec; state probe; `prepareSession` against a fake HOME with `~/.config/opencode`, `~/.local/share/opencode`, `~/.pi` mounts none of them.
- Integration test: a file in project A's data dir is seen by a later session of A and not by B; all probes pass. Full `-tags integration` suite passes.
- Real run against llama-swap in throwaway projects under `~/Library/Caches`: `opencode run` in A (63 probes passed); a later run of A lists the session, B lists none; `opencode run -s <id>` in another run resumed it and the model recalled its earlier reply; writing the config inside the sandbox fails with "Read-only file system".

Not verified: that our permissions outrank a hostile `~/.config/opencode/opencode.json` written mid-session (opencode's debug output doesn't show merged permissions; issue 09 tests the prompts). The interactive TUI resume was not tried; `opencode -c` is documented.

Notes: `localllm build` is required. A volume created by an older image is root-owned; the probe catches it and the README says to remove it. Volumes accumulate until removed (issue 16). The first one-off opencode CLI call in a fresh container can return empty results while its background service starts.
