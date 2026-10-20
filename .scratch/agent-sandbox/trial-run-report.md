# Trial run report (issue 17)

A hands-on trial of the sandbox on a real project, run on 2026-10-15. kota was still called `localllm` then, so the commands below use the old name.

- **Project:** a personal TypeScript project (the working repo, not a fresh clone), with a fake `API_KEY` in `.env` to test masking.
- **Model:** `qwen3.6-coder` behind llama-swap, the OpenAI-compatible gateway.
- **Agents:** opencode in ask mode and in full auto (`--auto`), and pi.

## What was checked

| Step | Command | Result |
|------|---------|--------|
| Containment check | `localllm check` | All 71 probes pass in 2 s, IPv6 included. `.env` and `.npmrc` are masked. A warning that `.npmrc` is tracked in git. |
| Dependencies | `localllm prep` | Asks on first run, listing `npm ci` and no install scripts. Doesn't ask again when nothing changed. Asks and lists the change after a `package.json` edit. First prep 12 s. |
| opencode, ask mode | `localllm run` | Real task done: edited `package.json`, asked before `npm test`, tests passed offline. `cat .env` reads empty. The change report lists `package.json`. |
| opencode, full auto | `localllm run --auto` | Lists uncommitted changes and asks for `y`. `n` aborts before a container starts. After committing, a task completes with no prompts. |
| pi | `localllm run --agent pi` | Warns that pi is always full auto. Real task done. |
| Tool calling | both agents | Works. No prose-only answers or raw tool-call JSON, so no llama-swap change was needed. |
| Shell protections | `localllm shell` | `.env` is empty, writes to `.git/hooks` and `.git/config` fail, no internet, the gateway answers. |
| Interrupts | Ctrl-C at the `--auto` prompt | Prints `interrupted`, exits 130, leaves no containers or placeholder files. The next run starts normally. |
| Broken manifest | `localllm run` with a broken `package.json` | Warns that dependencies can't be worked out and still starts. Prep works again once the file is fixed. |
| Clean up | `localllm clean` | Lists and removes the deps, opencode and pi volumes after `y`. Shared caches stay. |

Not tried: opening `shell` with a broken manifest, and checking that it still has the last prep's `node_modules`.

## Findings

1. **Agent commits capture mask placeholders.** pi committed with `git add -A`, which swept in the empty placeholders, the masked `.env` and `.npmrc`, and a file the host ignores only through the global git excludes. Nothing leaked, but pushing the commit would have damaged the repo. Fixed in [26](issues/26-agent-commits-capture-masks.md).
2. **The change report didn't say what changed.** A session added `wrangler` to `package.json` without updating the lock file, so the next `npm ci` failed, and the report only said "modified package.json". Fixed in [27](issues/27-change-report-shows-what-changed.md).
3. **Output problems.** Probe columns out of line for long names, a garbled Docker Compose progress line, and the prep error buried after pages of npm output. Fixed in [28](issues/28-output-polish.md).
4. **The `.npmrc` warning said "secret"** for a file holding only settings, and masking it also hid those settings from npm. Prep now reads the project-root `.npmrc` ([prep-npmrc 01](../prep-npmrc/issues/01-prep-reads-project-npmrc.md)).
5. **Ctrl-C inside the agent exits with 1.** Once the agent has the terminal, Ctrl-C reaches it as a keystroke. The agent exits 130, and kota reports that as an error and exits 1. Not filed.
6. **A cold model hangs the first message.** The first message spun until cancelled while llama-swap loaded the model, with no sign of what was happening. Not filed.

Also seen: opencode's file tools skip gitignored files, so it reports a masked `.env` as missing rather than empty. No slow steps, and the agents needed no tools the image lacks.
