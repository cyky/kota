# 29 — `--no-history`: run the Session on a No-history copy

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

Today the agent sees the project's whole git history. `.git` is mounted in place so git works, and masks don't reach it, so anything ever committed can be read with `git log -p`, including a secret that is masked or deleted now. `.kotaignore` can't help, because `.git` is never searched for mask rules.

`kota run --no-history [dir]` (and `shell`) runs the **Session** on a **No-history copy** instead: a throwaway clone of the project's current commit, with no older history, remotes or config. This issue makes the copy and runs the Session on it. The copy is always kept and its path printed; bringing the agent's commits back as a **Returned branch** is issue 31.

The manual version works today (see Comments). This issue makes it one flag.

## Agent Brief

**Category:** enhancement
**Summary:** `run --no-history` and `shell --no-history` clone the project's `HEAD` into a throwaway **No-history copy** in an offline container, and run the **Session** on it with the original project's config, **Prep**, volumes and **Engine VM**, but a separate agent state.

**Current behavior:**
- The project directory, `.git` included, is mounted read-write at the workspace. Every commit ever made is readable inside the **Session**.
- Everything kota keeps for a project is keyed by the project path: the state volume (`sandbox.StateVolume`), dependency volumes (`sandbox.DepVolume`), the prep record (`hostdata.PrepRecord`), Engine images and the **Engine VM**, the lease (`sessionregistry`), and session dirs.
- `.kotaignore`, `kota.yaml` and `kota.Dockerfile` are read from the project root.

**Desired behavior:**

1. **Flag.** `run` and `shell` accept `--no-history`. `prep`, `check` and `clean` don't: `prep` and `clean` act on the original project, and both kinds of **Session** use the result.

2. **Before the copy.** Refuse if the project isn't a git repository, or its `HEAD` is unborn. Hold the original's lease for the whole **Session**, as a normal **Session** does. The copy needs no lease of its own.
   Then one prompt, default no, lists everything that won't be in the copy:
   - uncommitted changes, from `approval.ReadStatus` (modified, staged, untracked but not ignored);
   - submodules (a `.gitmodules` file);
   - LFS-tracked paths (`filter=lfs` in a `.gitattributes`).
   "These are not in the copy. Continue without them? [y/N]". With `--auto`, this replaces the full-auto prompt, since it lists the same changes. Ignored files (`.env`, local overrides) aren't listed. The README says the copy holds only committed files.

3. **Making the copy.** In an offline container of the base image, with the original mounted read-only and the copies dir writable, run `git clone --depth 1 --single-branch --template= file://<original> <copy>`, then `git remote remove origin` and `git reflog expire --expire=now --all` in the copy, so nothing in it names the original's path or remotes. git never runs on the host against the original for this. `git clone` runs `upload-pack` against the original's `.git`, whose config an agent may have written in an earlier normal **Session**. `offlineRunner` mounts everything read-only, so this needs a variant with one writable mount.
   - **Where:** `<cache>/kota/copies/<sandbox.ProjectName(original)>-<YYYY-MM-DD-HHMMSS>/`, the timestamp being the **Session** start. It's validated by `pathpolicy` with the copies dir as its allowed root: the forbidden-path, home and symlink checks apply as usual. It must not overlap the original.
   - `hostdata.ProjectData` lists the original's kept copies, so `kota clean <original>` removes them.

4. **Running the Session.** The copy is mounted at the workspace instead of the original. Everything else uses the **original**:
   - mask rules, `kota.yaml` and `kota.Dockerfile` are read from the original's files, applied to the copy's tree, and the original's files are mounted read-only over the same paths in the copy, as protected files are today. A committed `.kotaignore` from `HEAD` never replaces the original's. **This matters for security:** an untracked `.kotaignore` that masks a tracked secret must still mask it in the copy.
   - dependency volumes, the prep record and prep gate, the project image, Engine images and the **Engine VM** are the original's;
   - git identity (`gitEnv`) comes from the original, as today;
   - the agent state volume is separate: `sandbox.StateVolume(original, agent+"-nohistory")`, so transcripts from normal **Sessions** (which may hold `git log -p` output) never reach it, and the agent can still resume its own `--no-history` **Sessions**. The name matches `sandbox.ProjectVolumes`, so `clean` already removes it. Check that.

   The code today uses one project path for both what is mounted and what things are keyed by. Split those two: the workspace dir (the copy) and the project identity (the original).

5. **After the Session.** The **Change report** runs as today, against the copy: watched files in its working tree, and issue 26's commits and warnings. Then: "copy kept at <path>", with a by-hand fetch hint. In this issue the copy is always kept. Issue 31 adds the **Returned branch** and deletes the copy when nothing in it needs you. On an interrupt, the copy is kept too.

**Key interfaces:**
- `parseOfflineArgs` / `sessionArgs`, `prepareSession`, `lockProject` in `cmd/kota/main.go`: the flag, and the split between workspace dir and project identity
- `approval.ReadStatus`, `confirmFullAuto`: the "not in the copy" prompt
- `offlineRunner` / `docker.RunOffline`: a variant with one writable mount, for the clone
- `pathpolicy.Validate`: validation with the copies dir as allowed root
- `hostdata.Dirs` / `ProjectData`: the copies dir and kept copies
- `sandbox.StateVolume`, `mask.Plan`, `engine` config and `projectimage`: keyed by, or read from, the original

**Acceptance criteria:**
- [ ] `run --no-history` and `shell --no-history` start a **Session** on a copy where `git log` shows one commit and `git remote` shows none
- [ ] Nothing from the original `.git` is readable in the **Session**: an older commit's blob is missing (`git cat-file -e` fails), and the original's config, remotes, hooks and reflog aren't there
- [ ] The clone runs in an offline container; no git runs on the host against the original for it
- [ ] An untracked `.kotaignore` in the original that masks a tracked file masks it in the copy
- [ ] Uncommitted changes, submodules and LFS paths are listed in one prompt that defaults to no; with `--auto` there is only that prompt
- [ ] The **Session** uses the original's dependency volumes, prep and **Engine VM**, and a separate agent state volume that `clean <original>` removes
- [ ] The copy lives under `<cache>/kota/copies/`, its path is printed after the **Session**, and `clean <original>` removes it
- [ ] Integration test (fake HOME, never the real one): a project with a secret in an earlier commit; inside a `shell --no-history` **Session**, `git log -p` doesn't show it and `git log` shows one commit
- [ ] README: what `--no-history` hides and doesn't, that the copy holds only committed files (no ignored files, submodules or LFS content), where the copy lives and that it's kept (until issue 31), that config, prep and volumes come from the original, the separate agent state, and the by-hand fetch for now

**Out of scope:**
- Bringing commits back and deleting the copy (issue 31)
- Submodules and LFS content in the copy
- History-free `prep`

## Blocked by

None - can start immediately.

## Comments

### 2026-10-16

Asked for after discussing how to keep history from the agent. The manual workaround today:

```sh
git clone --depth 1 file://$HOME/Projects/app ~/Projects/app-agent
kota prep ~/Projects/app-agent
kota run ~/Projects/app-agent
git -C ~/Projects/app fetch ~/Projects/app-agent HEAD:agent-work
```

The last step runs host git against a repository the agent controlled, which is what this issue should avoid.

### 2026-10-16 (triage)

> *This was generated by AI during triage.*

Left in `needs-triage`. The "Decisions for triage" above need a `/grill-with-docs` session before this can be specified. The fetch-back step is especially sensitive, since git on the host must never run against a repository the agent controlled. The likely outcome is `ready-for-human`, or a split into smaller issues.

Do issue 26 first: its excludes file and commit check also apply to the commits brought back from the copy.

### 2026-10-16 (grilling)

> *This was generated by AI during a grilling session.*

Decisions, folded into the Agent Brief above and into issue 31:

1. **Throwaway copy**, made fresh for every **Session**. Refreshing a kept copy has its own hard cases (unreturned commits, uncommitted changes, a tampered `.git`).
2. **Keyed by the original**, except a separate agent state volume. Sharing state with normal **Sessions** would leak history through the agent's old transcripts.
3. **Uncommitted changes in the original:** list and ask, default no. Carrying them over would mix work in progress, possibly with the secret being hidden, into the agent's branch.
4. **Bringing commits back:** two offline containers with a bundle between them, and only `HEAD` comes back (issue 31).
5. **Location and lifetime:** `<cache>/kota/copies/`. Deleted after the **Session**, unless it has uncommitted changes, refs that weren't brought back, or the return failed (issue 31).
6. **kota config from the original.** A copy without an untracked `.kotaignore` would unmask tracked secrets.
7. **Returned branch** name: `kota/<agent>-<YYYY-MM-DD-HHMMSS>` (issue 31).
8. **Change report** contents (issue 31).
9. **No submodules or LFS** for now, listed in the same prompt as uncommitted changes.
10. **Split:** this issue runs the **Session** on the copy and always keeps it; issue 31 brings the work back.
