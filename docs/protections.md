# Protecting the host

The agent can write anything in the project, and you later build, open and commit that project on your machine. This page covers what keeps the agent from planting code that runs there, how secrets are masked, and what kota reports after a command. The overview, and why each of these is needed, is in [The sandbox](sandbox.md).

## Git hooks and config

The agent can't plant code that git, direnv or kota runs on the host later. These are read-only in the sandbox:

- `.git/hooks` and `.git/config`, and a `core.hooksPath` inside the project (husky and similar);
- `.git/commondir`, which would redirect git to another directory's config and hooks, and, for linked worktrees, `commondir` and `config.worktree`;
- each submodule's hooks, config and `.git` pointer file;
- every file in the project that git config includes (`include.path`, `includeIf.*.path`), from any config level and through any chain of includes, including conditional includes whose condition doesn't hold now, since the agent could make it hold;
- `.envrc` (direnv);
- kota's own files: `kota.yaml`, `.kotaignore` and `kota.Dockerfile`.

Each is mounted read-only over itself. A missing one (no `.envrc`, no hooks directory, no `commondir`) gets an empty read-only placeholder for the session, so the agent can't create it either, and the placeholder is removed from the host afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself too, so it can't be renamed away and replaced. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. The probes check each of these mounts before the session.

Git still works inside the sandbox. Commits use your host identity, passed in through `GIT_CONFIG_*`. Git's excludes file there is a read-only list of the session's placeholders and masks, so `git add -A` doesn't stage them. Your host's global excludes (`core.excludesFile`, `~/.config/git/ignore`) aren't passed in, since even their patterns can name private files; instead, kota warns when a commit adds a file they ignore (see [Commits](#commits)).

Limits:

- **Files that hooks call stay writable**, like husky's `.husky/pre-commit`, and so does an `.envrc` in a subdirectory. `run` reports changes to them (see [Changed files that run code](#changed-files-that-run-code)).
- **Git config can name a writable project script.** `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `core.askPass`, `sequence.editor`, `diff.external`, `gpg.program`, `credential.helper`, `alias.* = !...`, `filter.*.clean|smudge|process`, `diff.*.textconv|command` and `merge.*.driver` run a command. When one names a path inside the project, `run`, `shell`, `check` and `prep` warn with the key, the config file and the script, and a change to that script is reported after the session. Paths are found by splitting the command into words, so a script reached through a variable or another program's config isn't seen.
- **The agent can create a git repository of its own**: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. `git config --global safe.bareRepository explicit` closes the bare-repository case.
- **Linked worktrees and submodule checkouts opened on their own** have a `.git` file that points outside the project. It is read-only, and git doesn't work inside the sandbox.

## Secret masks

Secrets in the project are masked: inside the sandbox they read as an empty file or directory and can't be written, while the files on the host stay untouched. The default rules cover:

- `.env` and `.env.*`, except `.env.example`, `.env.sample`, `.env.template` and `.env.dist`;
- keys and certificates: `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, and `id_rsa*`, `id_dsa*`, `id_ecdsa*` and `id_ed25519*` except public keys (`*.pub`);
- `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, and Terraform state (`*.tfstate`, `*.tfstate.*`);
- `.aws/`, `.ssh/` and `.gnupg/` directories.

Add your own in `.kotaignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` unmasks a default. It is read-only in the sandbox. `kota check` lists the masks.

```gitignore
# .kotaignore
config/credentials.yml
secrets/
# prep needs the registry settings; the agent can read them then too
!.npmrc
```

Limits:

- **Masks hide files on disk, not what git has stored.** A secret that is tracked, or was ever committed, can be read from `.git` (`git log -p -- .env`), and a commit made in the sandbox would record a tracked one emptied. `run`, `shell`, `check` and `prep` warn about masked files that are tracked now, but history isn't checked, and a commit that empties one is reported. To fix it, remove the file from the index (`git rm --cached`) and from history (for example with `git filter-repo`), and rotate the secret, since earlier clones and pushes still have it. `--no-history` hides older commits but not the last one; see [Sessions without git history](no-history.md).
- **Masks are computed at launch.** A secret created on the host during a session isn't masked until the next one.
- **Dependency and build directories aren't searched** (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...), to keep launch fast.
- **`--ro` paths aren't masked.**

## Changed files that run code

Some files that run things can't be locked without breaking normal work, so kota reports them instead. Before `run`, `shell` or `prep`, it records them, and when the command ends it lists every one that was added, modified or deleted. They are, at any depth:

- manifests and build files: `package.json`, `go.mod`, `Makefile` (and `makefile`, `GNUmakefile`), `build.rs`, `justfile` (and `Justfile`, `.justfile`), `Taskfile.yml` and `Taskfile.yaml`, `*.nix`, `flake.lock` and the `.cargo/` directory;
- environment and tool versions: `.envrc`, mise's `mise.toml`, `.mise.toml`, `mise.local.toml` and `.mise.local.toml`, and `.tool-versions`;
- git and hooks: `.gitattributes`, `.gitmodules`, `.pre-commit-config.yaml`, and the `.husky/` and `.githooks/` directories;
- editors and CI: the `.vscode/`, `.idea/`, `.devcontainer/` and `.github/workflows/` directories.

Content is compared by hash, so restoring a timestamp doesn't hide an edit; permission changes and symlink targets count too. Under each file, the report says what changed or how to see it:

```
WARNING: the session changed 2 file(s) that can run code on the host. ...
  modified  .envrc
      see: git -C /home/me/app diff HEAD -- .envrc
  modified  package.json
      devDependencies: added wrangler ^4.0.0
      scripts: added postinstall (runs on install) "node x.js"
```

- A modified `package.json` lists each dependency and script added, removed or changed, with its old and new value. Scripts the install runs, such as `postinstall` and `prepare`, are marked `(runs on install)`.
- A modified `go.mod` lists changed `go`, `toolchain`, `require`, `replace` and `tool` lines.
- Any other file, lockfiles included, gets the `git` command that shows the change. kota doesn't print the diff itself, because the agent wrote the file. A manifest gets the command too when it can't be parsed, or when nothing above changed.
- Names and values are escaped and long ones cut, so an agent-written file can't send control sequences to your terminal.

Review the listed files before you build, install or open the project in an editor. Limits:

- **It's a warning, not a block**: the changes are already on the host.
- **Dependency and build directories are skipped**, as for masks, and so is `.git`, which the mounts protect.
- **Nothing is reported** by `check`, or when kota itself is killed outright (SIGKILL) mid-session.

## Commits

After `run`, `shell` and `prep` (an install script can commit as well as an agent can), kota also reports what happened to git. In a project with its own `.git` directory, it lists:

- the branches and tags that were created, moved or deleted;
- every new commit, on any branch or a detached `HEAD` (at most 20);
- when `HEAD` lost its previous commit (a reset, rebase, amend or branch switch), that commit, so you can get it back with `git reset --hard`.

It then warns about paths in those commits that need review:

- **a placeholder kota mounted, or a masked secret**, added, deleted, modified or emptied. The real files are still on disk, so the report prints the `git rm --cached` or `git add` that fixes the next commit;
- **a file your host's global git excludes ignore**, added. Those excludes are read from your global git config only, never the project's;
- **a watched file changed in a commit but not in the working tree**, for example a workflow edited, committed and then put back. The change runs once the commit is checked out, merged or pushed.

None of this changes kota's exit status, and kota never amends, reverts or blocks a commit. git doesn't run on the host against the project for this: refs and commits are read in a throwaway container, offline and read-only, with textconv, external diff drivers and fsmonitor off. Remote-tracking refs and stashes aren't checked, and merge commits list no files of their own.
