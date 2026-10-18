# Protecting the host

What keeps the agent from planting code that runs on the host later, how secrets are masked, and what kota reports after a command. Why each of these is needed, and the overview, is in [The sandbox](sandbox.md).

## Git hooks and config

The agent can't plant code that runs later on the host. `.git/hooks`, `.git/config` and `.envrc` are mounted read-only over themselves; a missing `.envrc` or hooks directory is replaced by an empty read-only placeholder, which is removed from the host after the session. A `core.hooksPath` inside the project (husky and similar) is read-only too, as are the hooks, config and `.git` pointer file of each submodule. `.git/commondir`, which would redirect git to another directory's config and hooks, is read-only too. When it is missing it is created with `.` (meaning "this git dir") for the session and removed afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself so it can't be renamed away and replaced. Linked worktrees' `commondir` (and `config.worktree` when enabled) are protected the same way. So is every file inside the project that git config includes (`include.path`, `includeIf.*.path`), from any config level (system, global, the repository's, a submodule's) and through any chain of includes. Conditional includes count even when their condition doesn't hold now, since the agent could make it hold. A missing include target gets a placeholder. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. Git still works inside the sandbox: commit, branch and diff use your host identity, passed in through `GIT_CONFIG_*`. Git's excludes file there is a read-only list of the session's placeholders and masks, so `git add -A` doesn't stage them. Your host's global excludes (`core.excludesFile`, `~/.config/git/ignore`) don't apply inside the sandbox; they aren't passed in, since even their patterns can name private files. kota warns instead when a commit adds a file they ignore (see below). The probes check each of these mounts. Limits:

- The agent can create a git repository of its own: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. Set `git config --global safe.bareRepository explicit` to close the bare-repository case.
- In a linked worktree or a submodule checkout opened on its own, `.git` is a file that points outside the project. The file is read-only, and git does not work inside the sandbox.
- Files that hooks call, like husky's `.husky/pre-commit`, stay writable. So does an `.envrc` in a subdirectory. `run` warns when these change (see below).
- Git config values that run a command can name a project script, which stays writable: `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `core.askPass`, `sequence.editor`, `diff.external`, `gpg.program`, `credential.helper`, `alias.* = !...`, `filter.*.clean|smudge|process`, `diff.*.textconv|command` and `merge.*.driver`. When one names a path inside the project (as its command, or as an argument that exists), `run`, `shell`, `check` and `prep` warn with the key, the config file and the script, and a change to that script is reported after the session. Paths are found by splitting the command into words, so a script reached through a variable or another program's config isn't seen.

## Secret masks

Secrets are masked: inside the sandbox they read as an empty file or directory and can't be written, while the host files stay untouched. The default rules cover `.env` and `.env.*` (except `.env.example` and similar), keys and certificates (`*.pem`, `*.key`, `id_rsa*`, ...), `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, Terraform state, and `.aws/`, `.ssh/` and `.gnupg/` directories. Add your own in `.kotaignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` can unmask a default, and it is read-only inside the sandbox. `kota check` lists the masks. Limits:

- Masks are computed at launch. A secret created on the host during a session isn't masked until the next one.
- Dependency and build directories (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...) aren't searched, to keep launch fast.
- Masks don't reach git. A secret that is tracked, or was ever committed, can be read from `.git` inside the sandbox (`git log -p -- .env`), and a commit made there would record a tracked one emptied. `run`, `shell`, `check` and `prep` warn about masked files that are tracked now, but history isn't checked. A commit that empties one is reported after the session (see below). To fix it, remove the file from the index (`git rm --cached`) and from history (for example with `git filter-repo`), and rotate the secret, since earlier clones and pushes still have it. To keep the older history out of a session, use `--no-history` (see [Sessions without git history](no-history.md)); it keeps the last commit, so a masked file that is tracked now is still readable there.
- `--ro` paths aren't masked.

## Changed files that run code

Other files that run things can't be locked without breaking normal work, so `run` reports them instead. It records them before the session and, when it ends, lists any that were added, modified or deleted. The list covers, at any depth, `package.json`, `go.mod`, `Makefile`, `*.nix`, `flake.lock`, `.envrc`, mise's `mise.toml`, `.mise.toml`, `mise.local.toml` and `.tool-versions`, `.gitattributes`, `.gitmodules`, `build.rs`, `justfile` and `Justfile`, `Taskfile.yml`, `.pre-commit-config.yaml`, and the `.cargo/` and `.idea/` directories. It also covers the `.github/workflows/`, `.vscode/`, `.devcontainer/`, `.husky/` and `.githooks/` directories (`changereport.Watched`). Content is compared by hash, so restoring a timestamp doesn't hide an edit. Permission changes and symlink targets count too. Under each file the report says what changed, or how to see it:

- A modified `package.json` lists each dependency (`dependencies`, `devDependencies`, `optionalDependencies`, `peerDependencies`) and script that was added, removed or changed, with its old and new value. Scripts that the install runs, such as `postinstall` and `prepare`, are marked `(runs on install)`.
- A modified `go.mod` lists changed `go`, `toolchain`, `require`, `replace` and `tool` lines.
- Any other file, including lockfiles, gets the command that shows the change: `git -C <project> diff HEAD -- <path>`, or `git status` for an added file. kota doesn't print the diff itself, because the file was written by the agent. A manifest gets the command too when it can't be parsed before or after the session, or when none of the lines above changed.
- Names and values are escaped and long ones are cut, so an agent-written file can't send control sequences to your terminal. Manifest contents are read on the host before the session starts.

```
WARNING: the session changed 2 file(s) that can run code on the host. ...
  modified  .envrc
      see: git -C /home/me/app diff HEAD -- .envrc
  modified  package.json
      devDependencies: added wrangler ^4.0.0
      scripts: added postinstall (runs on install) "node x.js"
```

Review the listed files before you build, install or open the project in an editor. Limits:

- It's a warning, not a block: the changes are already on the host.
- Dependency and build directories are skipped, as for masks. So is `.git`, which is protected by mounts.
- `check` doesn't report changes, and nothing is reported if `kota` itself is killed outright (SIGKILL) mid-session.

## Commits

Commits are reported too, after `run`, `shell` and `prep` (a dependency install script can commit as well as an agent can). In a project with its own `.git` directory, kota records every local branch and tag and `HEAD` before the command, and after it lists the branches and tags that were created, moved or deleted, and every commit reachable now that wasn't before, on any branch or a detached `HEAD` (at most 20 are listed). When `HEAD` no longer contains its previous commit (a reset, rebase, amend or branch switch), the report says so and names the commit, so you can get it back with `git reset --hard`. It then warns about paths in those commits that need review:

- a placeholder kota mounted, or a masked secret: added, deleted, modified or emptied. The files still have their real content on disk, so the report prints the `git rm --cached` or `git add` that fixes them in a new commit.
- a file your host's global git excludes ignore, added. Those excludes are read from your global git config only, never the project's, and matched on the host.
- a watched file (the list above) changed in a commit when the working tree doesn't show it, for example a workflow edited, committed and then put back. The change runs once the commit is checked out, merged or pushed. Watched files the working tree report already lists aren't repeated.

None of this changes kota's exit status, and kota never amends, reverts or blocks a commit. Git doesn't run on the host against the project for this: the refs and commits are read in a throwaway container of the base image, offline and read-only, with textconv, external diff drivers and fsmonitor off. Remote-tracking refs and stashes aren't checked, and merge commits list no files of their own.
