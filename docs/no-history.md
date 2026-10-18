# Sessions without git history

Inside a normal session the agent can read the project's whole git history, including a secret that was committed once and is masked or deleted now. `kota run --no-history [dir]` (or `shell --no-history`) runs the session on a throwaway copy of the project instead, and brings the agent's commits back as a branch afterwards.

The project must be the top of a git repository with at least one commit.

## What it hides, and what it doesn't

The copy is a git repository with one commit: the project's last commit, as it is. It has none of the older commits, and none of the project's remotes, git config, hooks or reflog.

It doesn't hide:

- **The last commit.** `git log` in the copy shows it with its hash, message, author, email and date. Every file in it is readable through git, masked or not: masks only hide files on disk, so `git show HEAD:old/config.py` reads a masked file that is tracked. To keep a path and its history away from the agent, it must not be in the last commit.
- **What the agent learned earlier.** Anything it read in an earlier normal session and wrote into the project is in the copy too. (Its own transcripts are not: `--no-history` sessions have a separate state volume.)

## In a monorepo: point kota at a subdirectory

To keep the rest of a monorepo, and all its history, away from the agent, point kota at the subdirectory you work in (`kota run ~/Projects/mono/new-app`) instead of using `--no-history`. Only that directory is mounted, and the repository's `.git` is above it, so the agent gets no git history at all, and nothing outside the directory.

The cost: without `.git` the agent can't commit, so its work stays as uncommitted changes in your working tree, for you to review and commit. Code the project needs from elsewhere in the monorepo, such as shared libraries or a root workspace file, isn't there either.

## Before the session

The copy holds only what is committed. Before it is made, kota lists what won't be in it, and asks you to type `y`:

- uncommitted changes (modified, staged, or untracked and not ignored). With `--auto` this replaces the usual full-auto question: the agent works on the copy, so it can't overwrite your changes;
- submodules, which are empty directories in the copy;
- files stored in Git LFS, which are pointer files in the copy.

Ignored files (`.env`, local overrides) aren't listed, and aren't in the copy either.

## During the session

The session runs on the copy with the usual masks, protections, probes and Engine VM. Everything else belongs to the original project:

- `.kotaignore`, `kota.yaml` and `kota.Dockerfile` are taken from it, even when they aren't committed, so an untracked `.kotaignore` still masks a tracked secret in the copy. They are read-only in the copy, as in a normal session.
- `prep` runs on the original as usual, and the session uses its dependency volumes, prep record, project image, Engine VM and git identity.
- The project's lease is held for the whole session.

Only the agent's state is separate (see [Agent state](sessions.md#agent-state)), so transcripts from normal sessions, which may hold `git log -p` output, don't reach the copy.

## After the session

The commits on the copy's `HEAD` come back into the project as a branch, `kota/<agent>-<YYYY-MM-DDTHHMMSS>` (`kota/shell-...` after `shell`), named after the session's start. It is never checked out or merged: your working tree and current branch stay as they were, and you review and merge the branch yourself.

- Only `HEAD` comes back. Other branches and tags made in the copy are listed as not brought back.
- No branch is made when there are no new commits, or when the agent reset or replaced the history, so the branch wouldn't descend from the commit the copy was made from.

The change report lists the copy's branches, tags and commits with the usual warnings; since the copy's working tree is thrown away, a watched file changed in a commit is always flagged. Then it names the branch, or why there is none.

The copy is then deleted, unless something in it would be lost: uncommitted changes, branches or tags that weren't brought back, or commits that couldn't be. Then it is kept, and the report gives its path, the reason and the uncommitted changes. git in a kept copy reads config and hooks the agent could write, so look at them before you run git there. `kota clean [dir]` removes the project's kept copies, after listing them.

## How the copy and return are made

git never runs on the host against the project or the copy:

- **The copy** is made by `git clone --depth 1` in a throwaway container of the base image, offline, with the project mounted read-only. It lives in `copies/` in kota's data dir (`~/Library/Caches/kota/copies/<project>-<YYYY-MM-DDTHHMMSS>` on macOS), and must pass the same path rules as a project, with that directory as its allowed root.
- **The return** uses two more throwaway containers, offline, because the agent controlled the copy, its git config and hooks included. The first runs `git bundle create` in the copy, mounted read-only, and can write only an empty directory for the bundle. The second fetches the bundle into your repository with only its git directory writable, without the copy, and with hooks, fsmonitor and automatic gc off. It checks the objects (`transfer.fsckObjects`), and makes the branch only if it descends from the commit the copy was made from and doesn't exist yet.
