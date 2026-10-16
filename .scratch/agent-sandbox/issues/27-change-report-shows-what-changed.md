# 27 — Change report says what changed in watched files

Status: needs-triage

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Found in the issue 17 trial run. An agent session renamed the package in `package.json` and added `"wrangler": "^4.0.0"` to devDependencies, without updating `package-lock.json`. The change report on exit only said `modified package.json`. The next `kota prep` failed in `npm ci` because the lock file was out of date. The new dependency, which would have run code on the next install, was only found with `git diff`.

For a modified watched file, show what changed, or at least where to look:

- For manifests that prep understands (`package.json`, `go.mod`, ...), list dependency, script and lifecycle hook changes, as the prep gate already does.
- For other watched files, print a short diff, or the `git diff -- <path>` command to see it.

## Acceptance criteria

- [ ] An added, removed or changed dependency or script in a manifest is named in the change report
- [ ] Other modified watched files show a diff or a command to see one
- [ ] Tests cover a manifest dependency change and a non-manifest watched file

## Blocked by

None - can start immediately
