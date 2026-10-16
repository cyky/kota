# 31 — Return a `--no-history` Session's commits as a branch

Status: ready-for-agent
Category: enhancement

## Parent

`.scratch/agent-sandbox/PRD.md`

## What to build

After a `--no-history` **Session** (issue 29), the agent's commits are in the **No-history copy**, which is always kept, and the user fetches them by hand. That runs host git against a repository the agent controlled.

This issue brings the copy's `HEAD` back into the original project as a **Returned branch**, without running git in the copy with the original writable, and deletes the copy when nothing in it still needs the user.

## Agent Brief

**Category:** enhancement
**Summary:** After a `--no-history` **Session**, export the copy's new commits to a bundle in one offline container, import them as `kota/<agent>-<YYYY-MM-DD-HHMMSS>` in another, report them in the **Change report**, and delete the copy unless it holds something that wasn't returned.

**Current behavior (after issue 29):**
- The copy is always kept, at `<cache>/kota/copies/<project name>-<timestamp>/`, and its path is printed with a by-hand fetch hint.
- The **Change report** runs against the copy like a normal **Session**: watched files in its working tree, then issue 26's commits, refs and warnings.

**Desired behavior:**

1. **Export.** In an offline container of the base image, with the copy mounted read-only and an empty output dir (in the session dir) writable, run `git bundle create <out>/return.bundle HEAD ^<base>` in the copy. `<base>` is the original's `HEAD` at **Session** start. The agent's config may influence this step, but all it can write is that output dir. If `HEAD` didn't move, skip it.

2. **Import.** In a second offline container, with the original's git dir writable (found as `approval.FindRepo` does for a linked worktree), its working tree read-only, and the bundle read-only:
   - `git fetch <bundle> HEAD:refs/kota-incoming/<id>`, with the `gitPrefix` hardening plus `core.hooksPath=/dev/null` (fetch runs the `reference-transaction` hook), `gc.auto=0`, `maintenance.auto=false` and `fetch.writeCommitGraph=false`;
   - `git merge-base --is-ancestor <base> refs/kota-incoming/<id>`, checked against the original's own objects, not the copy;
   - if that holds and `refs/heads/kota/<agent>-<YYYY-MM-DD-HHMMSS>` doesn't exist, create it at the incoming commit. Delete the incoming ref either way.
   git never runs in the copy here, and never runs on the host against either repository. The branch is never checked out or merged: the user's working tree and current branch stay as they were.

3. **Branch name.** `kota/<agent>-<YYYY-MM-DD-HHMMSS>` (ISO date with separators, the **Session** start, the same timestamp as the copy's dir), for example `kota/pi-2026-10-16-143205`.

4. **No branch** when:
   - `HEAD` didn't move: "no commits to bring back";
   - `HEAD` doesn't descend from `<base>` (a reset, or a new root): "history rewritten, not brought back";
   - the export or import failed, or the name exists: "could not bring back: <reason>".

5. **The copy** is deleted after the return unless:
   - it has uncommitted changes (`approval.ReadStatus` on the copy, in the offline container);
   - it has branches or tags, other than the one `HEAD` is on, that were created or moved in the **Session**;
   - no branch was made although `HEAD` moved (the last two cases in 4).
   Then it's kept, and the report says where and why. On an interrupt, the return runs as part of cleanup, and the copy is kept if the return didn't finish.

6. **Change report** for a `--no-history` **Session**, replacing issue 29's:
   - the **Returned branch**, its name and commit count, then its commits as issue 26 lists them, with issue 26's warnings (masked, emptied, placeholder and globally ignored paths). Flagged commits are still brought back; warnings never change the exit status.
   - **Watched paths in the returned commits are always flagged**, since the copy's working tree is discarded. Issue 26 skips a path the working-tree report flagged; here there's no working-tree report of watched files.
   - or why there's no branch (4);
   - "not brought back:" other branches and tags created or moved in the copy;
   - "copy kept at <path> (<reason>)", with all uncommitted changes in the copy when that's the reason, not only watched files;
   - nothing about the original's working tree, which the **Session** couldn't reach.

   Example:

   ```
   returned branch kota/pi-2026-10-16-143205 (2 commits):
     a1b2c3d  Fix login redirect
     e4f5a6b  Update package.json          ! changes watched file package.json
   not brought back: branch wip-experiment
   copy kept at ~/Library/Caches/kota/copies/app-2026-10-16-143205 (uncommitted changes):
     M src/auth.ts
   ```

**Key interfaces:**
- the offline runner with a writable mount from issue 29: used for both containers
- `approval.FindRepo` / `ReadStatus`: the original's git dir mounts, and the copy's uncommitted changes
- `changereport.ReadRefs`, `History`, the commit warnings, and `commitCheck` in `cmd/kota/main.go`: the commit list and warnings, run against the copy
- `hostdata`: deleting the copy

**Acceptance criteria:**
- [ ] A commit made in a `--no-history` **Session** arrives in the original as `kota/<agent>-<YYYY-MM-DD-HHMMSS>`; the original's current branch and working tree are unchanged
- [ ] No git runs on the host against the original or the copy, and the import container has no mount of the copy
- [ ] A hook planted in the copy's `.git/hooks` and config planted in the copy's `.git/config` don't run during the return
- [ ] A `HEAD` that doesn't descend from the base makes no branch, keeps the copy, and says why
- [ ] The copy is deleted when everything was returned and nothing is uncommitted; kept, with path and reason, otherwise
- [ ] The **Change report** shows the sections above, and flags a watched file changed in a returned commit
- [ ] Integration test (fake HOME): the agent commits in a `shell --no-history` **Session**; the branch exists in the original afterwards with that commit, and the copy is gone. A second run leaves an uncommitted file, and the copy is kept and reported.
- [ ] README: the **Returned branch**, when the copy is kept, and the report; the by-hand fetch hint from issue 29 removed

**Out of scope:**
- Returning branches other than `HEAD`'s
- Merging or checking out the **Returned branch**
- Rewriting or blocking flagged commits

## Blocked by

- `29-no-history.md`

## Comments

### 2026-10-16 (grilling)

> *This was generated by AI during a grilling session.*

Split out of issue 29. Why two containers: the original's `.git` can't be made writable "only for the new ref", so any code the agent's config ran in a container with both mounted could plant a hook that the user's own git later runs. The bundle is plain data that `index-pack` checks, and the import container never sees the copy. See issue 29's grilling comment for the other decisions.
