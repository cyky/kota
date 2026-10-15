# Trial run checklist (issue 17)

Hands-on test of localllm on a real project, against the real llama-swap. Expect 30-45 minutes. Tick each box as you go and write what you saw under **Notes** for each section, especially anything that differs from **Expect**, felt slow, was confusing, or needed a tool the image doesn't have.

Host commands are written for fish. Throughout, `$P` is the trial project:

```fish
set P ~/Projects/trial
```

## 0. Setup

```fish
cd ~/Projects/personal/localllm
go install ./cmd/localllm
localllm build
```

- [ ] `localllm build` finishes. It rebuilds the base and forwarder images on the digest-pinned base images.
- [ ] llama-swap is running, and `curl -s http://localhost:8080/v1/models` lists `qwen3.6-coder`.
- [ ] A fresh clone of a real TypeScript or Go project is in place, so nothing important is at risk:

  ```fish
  git clone <repo> ~/Projects/trial
  ```

- [ ] A fake secret is in place, to test masking:

  ```fish
  echo 'API_KEY=supersecret' > $P/.env
  ```

Notes:

## 1. Containment check

```fish
localllm check $P
```

- [ ] Every probe passes, including `internet [2606:4700:4700::1111]:443` and `internet dns ipv6 example.com`.
- [ ] `.env` is listed among the masks.
- [ ] Any warnings make sense: tracked secrets, git config that runs project files, the project image.
- [ ] Time taken: ____ s

Notes:

## 2. Dependencies

```fish
localllm prep $P
```

- [ ] The first prep asks you to type `y`, listing the commands and any install scripts (project code).
- [ ] It finishes with "dependencies installed".
- [ ] Running `localllm prep $P` again doesn't ask, because nothing changed.
- [ ] After a small edit to `package.json` or `go.mod`, prep asks and lists the change. Answer `n`, then revert the edit.
- [ ] Time taken for the first prep: ____ s

Notes:

## 3. opencode in ask mode: one real task

```fish
localllm run $P
```

- [ ] The probes pass, the model is `qwen3.6-coder`, and `approval:` shows ask mode.
- [ ] A small real task is completed, for example "add a test for X" or "fix lint error Y".
- [ ] **Tool calling works:** the agent reads files, proposes edits and asks before running commands. If it only answers in prose or prints raw tool-call JSON, that is the `--jinja` problem. Copy an excerpt into Notes.
- [ ] When you ask it to run `cat .env`, the file reads as empty.
- [ ] When you ask it to run the tests (`npm test` or `go test ./...`), they run offline using the prepped dependencies.
- [ ] After exiting, a change report appears if it touched watched files (`package.json`, `Makefile`, `build.rs`, ...).
- [ ] Time from the command to the agent's prompt: ____ s

Notes:

## 4. opencode in full auto

Leave the edits from step 3 uncommitted, then run:

```fish
localllm run --auto $P
```

- [ ] It lists the uncommitted changes and asks you to type `y`.
- [ ] Answering `n` aborts, with no container started.
- [ ] After committing or stashing and running it again, a task completes without any approval prompts.

Notes:

## 5. pi: one real task

```fish
localllm run --agent pi $P
```

- [ ] It warns that pi always runs in full auto, then runs the probes.
- [ ] A small real task is completed.
- [ ] Tool calling works, as in step 3.

Notes:

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

- [ ] `.env` is empty.
- [ ] The hook and config writes fail.
- [ ] There is no internet access.
- [ ] The gateway answers.

Notes:

## 7. Interrupts and cleanup

- [ ] Ctrl-C during the probes of `localllm run $P` prints `localllm: interrupted` and exits with code 130 (`echo $status`).
- [ ] Nothing is left behind afterwards:
  - [ ] `git -C $P status` shows no new placeholder files.
  - [ ] `docker ps -a | grep localllm` shows no containers.
- [ ] Running `localllm run $P` again straight away starts normally, with no "busy" error.

Notes:

## 8. Broken manifest fallback

- [ ] Break `$P/package.json` by deleting a brace, then run `localllm shell $P`. A WARNING says the dependencies can't be worked out, and the shell still opens.
- [ ] Inside, `ls node_modules` shows the prepped dependencies, not your host's.
- [ ] After fixing the file and running `localllm prep $P`, prep works again.

Notes:

## 9. Clean up

```fish
localllm clean $P
```

- [ ] It lists the project's volumes and the prep record, and removes them after you type `y`.

Notes:

## Summary to send back

- Tool calling with opencode: works / doesn't (excerpt)
- Tool calling with pi: works / doesn't (excerpt)
- Steps that failed or surprised you, with output:
- Slow steps (build, check, prep, start of `run`):
- Tools the agent wanted but the image lacked:
- Other friction:
