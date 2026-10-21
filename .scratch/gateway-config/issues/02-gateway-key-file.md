# 02 — Each gateway's own key file

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/gateway-config/PRD.md`

## What to build

A named **Gateway** can set `keyFile:`, the path of a file that holds its key; the config never holds the key itself. For a named gateway, only its own `keyFile` is used, and `KOTA_GATEWAY_KEY` and `KOTA_GATEWAY_KEY_FILE` are ignored, so each key is sent only to the gateway it belongs to. A gateway given as a URL uses the environment variables as today.

The key file is read with today's rules for `KOTA_GATEWAY_KEY_FILE`: surrounding whitespace trimmed, an empty file is an error, the key must be printable ASCII without spaces, and no error quotes the key. Errors name the gateway. A `keyFile` path must be absolute or start with `~/`; a relative one is an error, found when the whole file is checked. Only the chosen gateway's key file is read, so a missing key for another gateway doesn't stop a Session. Where the file lives isn't enforced.

The environment-variable key loading moves into the resolver module from slice 01, which returns the key and its source. `kota check` shows the key's source (`keyFile <path>`, `KOTA_GATEWAY_KEY`, `KOTA_GATEWAY_KEY_FILE <path>`, or none), never the value. A 401 from the gateway still says how to set the key, now naming the gateway's `keyFile` for a named gateway.

## Acceptance criteria

- [ ] A named gateway's `keyFile` key reaches the gateway, as `KOTA_GATEWAY_KEY` does today
- [ ] For a named gateway, `KOTA_GATEWAY_KEY` and `KOTA_GATEWAY_KEY_FILE` are ignored; for a URL gateway they work as today
- [ ] Only the chosen gateway's key file is read
- [ ] An empty key file, a key with spaces or non-ASCII, and a relative `keyFile` path are errors naming the gateway, and no error contains the key
- [ ] `kota check` shows the key's source and never its value
- [ ] The 401 hint names the right way to set the key for the chosen gateway
- [ ] Unit tests cover each of the above with a temporary config dir and a fake environment
- [ ] `docs/sessions.md`, the README's settings table and `kota help` describe `keyFile` and when the environment variables apply

## Blocked by

- `.scratch/gateway-config/issues/01-named-gateways.md`
