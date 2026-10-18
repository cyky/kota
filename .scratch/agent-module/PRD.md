# PRD — One deep module per Agent

Category: refactor

## Problem

An **Agent** is spread across `cmd/kota/main.go` (`agentSetup`, `agentFor`, the in-sandbox path constants, `loadOverrides`, `writeExtensions`, the hand-copied override keys in `usage`), the `internal/opencode` and `internal/pi` packages (raw `Render*` parts and override policies that main.go wires together), and `internal/approval` (`alwaysAuto = {"pi": true}`).

Each **Agent override** feature (issues 35-38 in `.scratch/agent-sandbox`) changed `main.go`, `main_test.go` and the usage text, though only one agent's policy changed. `agentSetup.render` returns contents that must line up by index with `agentSetup.configs`, and `prepareSession` knows the note, extensions and inline-config paths by name. The override tests run through `prepareSession`, with a 7-variable fake HOME and host git.

## Solution

One package, `internal/agent`, with opencode and pi as its two unexported adapters behind one interface:

```go
// Load picks the agent and reads the user's Agent overrides. It writes nothing.
func Load(name, configHome string) (*Agent, error)

func (a *Agent) Name() string               // also the state volume name
func (a *Agent) Overrides() []string        // host paths found, for "agent override:"
func (a *Agent) FullAuto(auto bool) bool    // pi: always
func (a *Agent) Approval(auto bool) string  // derived from FullAuto
func (a *Agent) Prepare(dir string, note Note) (*Prepared, error)

type Note struct { Style instructions.Style; Engine string }

type Prepared struct {
    ReadOnly []sandbox.Mount    // configs, the note, extensions
    Scratch  []string
    Env      map[string]string  // including opencode's inline cli.json
    Command  []string
}
func (p *Prepared) Configure(c models.Catalog, auto bool, keyEnv string) error

const Default = "opencode"
func Names() []string
func OverrideHelp() string
```

- `Load` stays apart from `Prepare` so a bad override still fails before kota writes to the host.
- The agent owns `sessionDir/agent`: `Prepare` creates the config files (empty), the note (its own facts, via `instructions.Note`) and the extensions; `Configure` writes the rendered configs once the Gateway has given the model catalog. `Configure` can't be called before the files exist.
- Prep has no agent: `session.agent` is nil.
- `approval` takes a full-auto bool and names no agents.
- Usage text for overrides, `--agent` and the unknown-agent error come from the module.

Behaviour does not change. The Docker integration tests (`override_integration_test.go`, `instructions_integration_test.go`, the `InShell` tests) pass unchanged throughout.

## Decisions

Settled in a grilling session on 2026-10-18 (architecture review, candidate 1):

- One package, not `internal/opencode` + `internal/pi` + shared types: only the interface stays exported, policies and `Render*` become private.
- The agent writes its own files, rather than returning path-to-content pairs for main.go to write.
- Loading overrides, composing the note, writing extensions, the approval line, the default command and the state volume name all move in. The `--no-history` state suffix, the shell command and `kota run -- cmd` stay in main.go.
- Agent tests go through `Load`, `Prepare` and `Configure`, not the private renderers.
- The README keeps its own prose; a test checks it names every allowed override key.

## Out of scope

- Deriving the probe list from the mount list (architecture review candidate 2). Until then `main_test.go` checks that every `Prepared.ReadOnly` target is probed read-only.

## Issues

1. `issues/01-agent-package.md`
2. `issues/02-agent-owns-its-directory.md`
3. `issues/03-generated-agent-help.md`
