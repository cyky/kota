# Trial run checklist (issue 17)

Hands-on test of localllm on a real project, against the real llama-swap. Expect 30-45 minutes. Tick each box as you go and write what you saw under **Notes** for each section, especially anything that differs from **Expect**, felt slow, was confusing, or needed a tool the image doesn't have.

Host commands are written for fish. Throughout, `$P` is the trial project:

```fish
set P ~/Projects/trial
```

Run on 2026-10-15 against a personal TypeScript project (the real repo rather than a fresh clone). Findings are in the summary at the end.

## 0. Setup

```fish
cd ~/Projects/personal/localllm
go install ./cmd/localllm
localllm build
```

- [x] `localllm build` finishes. It rebuilds the base and forwarder images on the digest-pinned base images.
- [x] llama-swap is running, and `curl -s http://localhost:8080/v1/models` lists `qwen3.6-coder`.
- [x] A fresh clone of a real TypeScript or Go project is in place, so nothing important is at risk:

  ```fish
  git clone <repo> ~/Projects/trial
  ```

- [x] A fake secret is in place, to test masking:

  ```fish
  echo 'API_KEY=supersecret' > $P/.env
  ```

Notes: `~/go/bin` wasn't on PATH in fish; `fish_add_path ~/go/bin` fixes it.

## 1. Containment check

```fish
localllm check $P
```

- [x] Every probe passes, including `internet [2606:4700:4700::1111]:443` and `internet dns ipv6 example.com`.
- [x] `.env` is listed among the masks.
- [x] Any warnings make sense: tracked secrets, git config that runs project files, the project image.
- [x] Time taken: 2 s

Notes: 71 passed (probes 380 ms). Masks: `.env`, `.npmrc`. Warned that `.npmrc` is tracked in git; it holds only settings, no token.

## 2. Dependencies

```fish
localllm prep $P
```

- [x] The first prep asks you to type `y`, listing the commands and any install scripts (project code).
- [x] It finishes with "dependencies installed".
- [x] Running `localllm prep $P` again doesn't ask, because nothing changed.
- [x] After a small edit to `package.json` or `go.mod`, prep asks and lists the change. Answer `n`, then revert the edit.
- [x] Time taken for the first prep: 12 s, including answering `y` (`npm ci` 3 s)

Notes: listed `npm ci` and no install scripts, which is correct; it has none in project code.

## 3. opencode in ask mode: one real task

```fish
localllm run $P
```

- [x] The probes pass, the model is `qwen3.6-coder`, and `approval:` shows ask mode.
- [x] A small real task is completed, for example "add a test for X" or "fix lint error Y".
- [x] **Tool calling works:** the agent reads files, proposes edits and asks before running commands. If it only answers in prose or prints raw tool-call JSON, that is the `--jinja` problem. Copy an excerpt into Notes.
- [x] When you ask it to run `cat .env`, the file reads as empty.
- [x] When you ask it to run the tests (`npm test` or `go test ./...`), they run offline using the prepped dependencies.
- [x] After exiting, a change report appears if it touched watched files (`package.json`, `Makefile`, `build.rs`, ...).
- [x] Time from the command to the agent's prompt: fast, not timed

Notes: edited `package.json`, asked before `npm test`, tests passed; the change report listed `package.json`. The first message hung on a cold model. opencode first said `.env` didn't exist; an explicit `cat` showed it empty.

## 4. opencode in full auto

Leave the edits from step 3 uncommitted, then run:

```fish
localllm run --auto $P
```

- [x] It lists the uncommitted changes and asks you to type `y`.
- [x] Answering `n` aborts, with no container started.
- [x] After committing or stashing and running it again, a task completes without any approval prompts.

Notes: edited the README with no prompts.

## 5. pi: one real task

```fish
localllm run --agent pi $P
```

- [x] It warns that pi always runs in full auto, then runs the probes.
- [x] A small real task is completed.
- [x] Tool calling works, as in step 3.

Notes: edited the README, then committed with `git add -A` on its own, sweeping in the mask placeholders. See finding 1. The commit was undone with `git reset HEAD~1`.

## 6. Shell and protections

```fish
localllm shell $P
```

Inside the shell (the container's shell, not fish), check each of these:

```sh
cat .env                                                # empty
touch .git/hooks/pre-commit                             # fails: read-only
echo x >> .git/config                                   # fails
curl -m 3 https://example.com                           # fails: no network
curl -fsS http://gateway:8080/v1/models | head -c 200   # works
exit
```

- [x] `.env` is empty.
- [x] The hook and config writes fail.
- [x] There is no internet access.
- [x] The gateway answers.

Notes: all passed.

## 7. Interrupts and cleanup

- [x] Ctrl-C during the probes of `localllm run $P` prints `localllm: interrupted` and exits with code 130 (`echo $status`).
- [x] Nothing is left behind afterwards:
  - [x] `git -C $P status` shows no new placeholder files.
  - [x] `docker ps -a | grep localllm` shows no containers.
- [x] Running `localllm run $P` again straight away starts normally, with no "busy" error.

Notes: the probes take 0.4 s, so the exit code was checked with Ctrl-C at the `--auto` prompt instead. Ctrl-C after the agent starts exits with code 1 instead; see finding 3.

## 8. Broken manifest fallback

- [ ] Break `$P/package.json` by deleting a brace, then run `localllm shell $P`. A WARNING says the dependencies can't be worked out, and the shell still opens.
- [ ] Inside, `ls node_modules` shows the prepped dependencies, not your host's.
- [x] After fixing the file and running `localllm prep $P`, prep works again.

Notes: the warning was checked with `localllm run`, not `shell`; `ls node_modules` was not tried. Prep first failed because an agent had added `wrangler` without updating the lock file (finding 2); it worked after the change was discarded.

## 9. Clean up

```fish
localllm clean $P
```

- [x] It lists the project's volumes and the prep record, and removes them after you type `y`.

Notes: removed the deps, opencode and pi volumes; the shared caches remain.

## Summary to send back

- Tool calling with opencode: works, in both ask mode and full auto. No prose-only answers or raw tool-call JSON.
- Tool calling with pi: works.
- Steps that failed or surprised you:
  1. **Agent commits capture mask placeholders.** pi's `git add -A` committed the empty placeholders for `.env`, `.envrc`, `.localllmignore`, `localllm.Dockerfile` and `localllm.yaml`, an emptied `.npmrc`, and `.claude/settings.local.json`. That last file is ignored on the host only through `~/.config/git/ignore`, which the sandbox doesn't have. Nothing leaked, but pushing that commit would damage the repo, and the change report doesn't look at commits.
  2. **Agents edit more than asked, and the change report doesn't say what.** A session renamed the package and added `wrangler` without updating the lock file, so `npm ci` failed in prep. The report only said "modified package.json".
  3. **Ctrl-C inside the agent exits with code 1.** Once the agent has the terminal, Ctrl-C reaches it as a keystroke. The agent exits 130, and localllm prints `localllm: exit status 130` and exits 1 (`cmd/localllm/main.go:126`).
  4. **Cold model.** The first message spun until cancelled; the second worked. Probably llama-swap was loading the model. There's no warm-up or "loading" message.
- Slow steps: none.
- Tools the agent wanted but the image lacked: none seen.
- Other friction:
  - The `.npmrc` warning says to change "the secret" even when the file holds no token. Masking it also hides its settings from npm in the sandbox.
  - On a failed prep, localllm's error line comes after pages of npm usage text.
  - Probe output columns don't line up for long names.
  - The Docker Compose progress line is garbled every time (`[+]  1/1t 1/11`).
  - opencode's file tools skip gitignored files, so it reports a masked `.env` as missing.
