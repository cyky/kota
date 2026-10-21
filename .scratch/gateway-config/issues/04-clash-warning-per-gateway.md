# 04 — Model-clash warning only for Sessions on the same gateway

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/gateway-config/PRD.md`

## What to build

When a Session starts with a different model from another running Session, kota warns that llama-swap will swap models back and forth. With more than one **Gateway**, that warning is wrong for Sessions on different gateways. The session registry records each Session's gateway URL, and the warning compares only Sessions on the same URL. It stays a warning and never stops a Session. Its wording no longer assumes llama-swap: "a gateway that loads one model at a time (such as llama-swap) will swap models back and forth".

The gateway URL exists today, so this slice doesn't need named gateways.

## Acceptance criteria

- [ ] Sessions on different gateway URLs never get the clash warning; Sessions on the same URL get it as today
- [ ] The warning has the new wording and still doesn't stop the Session
- [ ] A registry entry written by an older kota, without a gateway URL, doesn't break the check
- [ ] The registry's clash test covers same and different gateway URLs
- [ ] Any doc that quotes the warning is updated

## Blocked by

None - can start immediately
