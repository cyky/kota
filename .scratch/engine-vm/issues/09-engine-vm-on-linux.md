# 09 — Engine VM on Linux

Status: ready-for-human

## Parent

`.scratch/engine-vm/PRD.md`

## What to build

Run the **Engine VM** containment and end-to-end integration tests on a Linux machine with KVM. Fix anything macOS-specific that slipped into the engine code. On a machine without `/dev/kvm`, an engine project's **Session** refuses to start with a clear message, instead of starting a VM too slow to use.

Making the rest of localllm Linux-ready (the LAN gateway probe, `host.docker.internal`) is out of scope.

## Acceptance criteria

- [ ] Engine containment and end-to-end integration tests pass on Linux with KVM
- [ ] Missing `/dev/kvm` gives a clear refusal
- [ ] README notes the Linux requirements for the **Engine VM**

## Blocked by

- `03-engine-probes-and-refusal.md`
