# Protecting the host

The agent can write anything in the project, and you later build, open and commit that project on your machine. This page covers what keeps the agent from planting code that runs there, how secrets are masked, and what kota reports after a command. The overview, and why each of these is needed, is in [The sandbox](sandbox.md).

## Git hooks and config

The agent can't plant code that git, direnv or kota runs on the host later. These are read-only in the sandbox:

- `.git/hooks` and `.git/config`, and a `core.hooksPath` inside the project (husky and similar);
- `.git/commondir`, which would redirect git to another directory's config and hooks, and, for linked worktrees, `commondir` and `config.worktree`;
- each submodule's hooks, config and `.git` pointer file;
- every file in the project that git config includes (`include.path`, `includeIf.*.path`), from any config level and through any chain of includes, including conditional includes whose condition doesn't hold now, since the agent could make it hold;
- `.envrc` (direnv);
- kota's own files: `kota.yaml`, `.kotaignore` and `kota.Dockerfile`;
- the `.agents/skills` directory, when `kota.yaml` loads the [Project skills](sessions.md#project-skills) and the session wasn't started with `--skills-writable`. A symlink inside it whose target isn't inside it, or is absolute, is refused too, and a missing one is created empty for the session rather than held by a placeholder.

Each is mounted read-only over itself, and files are locked on the host for the session (see [Locked files on the host](#locked-files-on-the-host)). A missing one (no `.envrc`, no hooks directory, no `commondir`) gets an empty read-only placeholder for the session, so the agent can't create it either, and the placeholder is removed from the host afterwards. `.git`, and every other directory on the way to a protected path, is mounted over itself too, so it can't be renamed away and replaced. A protected path with a symlink anywhere along it is refused, because a read-only mount can't hold it in place. The probes check each of these mounts before the session.

The shared Go module and corepack caches, which every project's sessions use, are read-only as well, so the agent can't plant code in another project's dependencies; the probes check them too, and `kota check` lists them. Only `prep`'s fetch step writes them (see [Shared caches](dependencies.md#shared-caches)).

Git still works inside the sandbox. Commits use your host identity, passed in through `GIT_CONFIG_*`. Git's excludes file there is a read-only list of the session's placeholders and masks, so `git add -A` doesn't stage them. Your host's global excludes (`core.excludesFile`, `~/.config/git/ignore`) aren't passed in, since even their patterns can name private files; instead, kota warns when a commit adds a file they ignore (see [Commits](#commits)).

Limits:

- **Files that hooks call stay writable**, like husky's `.husky/pre-commit`, and so does an `.envrc` in a subdirectory. `run` reports changes to them (see [Changed files that run code](#changed-files-that-run-code)).
- **Git config can name a writable project script.** `core.fsmonitor`, `core.sshCommand`, `core.editor`, `core.pager`, `core.askPass`, `sequence.editor`, `diff.external`, `gpg.program`, `credential.helper`, `alias.* = !...`, `filter.*.clean|smudge|process`, `diff.*.textconv|command` and `merge.*.driver` run a command. When one names a path inside the project, `run`, `shell`, `check` and `prep` warn with the key, the config file and the script, and a change to that script is reported after the session. Paths are found by splitting the command into words, so a script reached through a variable or another program's config isn't seen.
- **The agent can create a git repository of its own**: `git init` in a non-git project, a nested repository in a subdirectory, or bare-repository files at the project root, which host git falls back to if `.git/HEAD` is broken. Their hooks and config run when you use git there on the host. `git config --global safe.bareRepository explicit` closes the bare-repository case. kota's own git calls on the host (reading your identity, the config and the tracked files before a session) don't fall back: they name `.git` explicitly, refuse a bare repository they would only have found by looking, and turn fsmonitor off, without changing any git config. A `.git` git can't read, such as one with a broken `HEAD`, refuses the next session with an error saying so.
- **Linked worktrees and submodule checkouts opened on their own** have a `.git` file that points outside the project. It is read-only, and git doesn't work inside the sandbox.

## Secret masks

Secrets in the project are masked: inside the sandbox they read as an empty file or directory and can't be written, while the files on the host stay untouched. The default rules cover:

- `.env` and `.env.*`, except `.env.example`, `.env.sample`, `.env.template` and `.env.dist`;
- keys and certificates: `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, and `id_rsa*`, `id_dsa*`, `id_ecdsa*` and `id_ed25519*` except public keys (`*.pub`);
- `.npmrc`, `.netrc`, `.pypirc`, `.git-credentials`, and Terraform state (`*.tfstate`, `*.tfstate.*`);
- `.aws/`, `.ssh/` and `.gnupg/` directories.

Add your own in `.kotaignore` at the project root, in gitignore syntax. It comes after the defaults, so `!` unmasks a default, except `.npmrc`: every `.npmrc` is masked whatever the rules say, and kota warns about a rule that unmasks one. `prep` reads the one at the project root instead (see [Private registries](dependencies.md#private-registries)). `.kotaignore` is read-only in the sandbox. `kota check` lists the masks.

```gitignore
# .kotaignore
config/credentials.yml
secrets/
# the example config holds no secrets
!config/example.pem
```

Limits:

- **Masks hide files on disk, not what git has stored.** A secret that is tracked, or was ever committed, can be read from `.git` (`git log -p -- .env`), and a commit made in the sandbox would record a tracked one emptied. `run`, `shell`, `check` and `prep` warn about masked files that are tracked now, but history isn't checked, and a commit that empties one is reported. To fix it, remove the file from the index (`git rm --cached`) and from history (for example with `git filter-repo`), and rotate the secret, since earlier clones and pushes still have it. `--no-history` hides older commits but not the last one; see [Sessions without git history](no-history.md).
- **Masks are computed at launch.** A secret created on the host during a session isn't masked until the next one.
- **Dependency and build directories aren't searched** (`node_modules`, `.venv`, `vendor`, `target`, `dist`, `build`, ...), to keep launch fast.
- **`--ro` paths aren't masked.**

## Locked files on the host

A read-only mount or a mask holds only while the file under it stays the same file on the host. If something on the host replaces it (writes a new file and renames it over, as host git does with `.git/config` and many editors do on save) or deletes it, the mount is dropped in the sandbox: a read-only file becomes writable from there, and a masked secret becomes readable. On Rancher Desktop (VZ, virtiofs) this happens about a second later, when the Docker VM's cached entry for the path runs out.

So on macOS, kota locks these files on the host for the length of a session, with the user-immutable flag (`chflags uchg`): every protected file, file placeholder, seeded file (`commondir`) and masked file. It creates file placeholders itself, empty, so they can be locked too. `run`, `shell`, `check` and `prep` say how many:

```
note: 9 protected and masked files are locked on the host until the session ends
```

While a session runs, anything that changes them on the host fails with "Operation not permitted": saving `.envrc`, `kota.yaml` or a masked `.env` in an editor, `git config`, and host git commands that write `.git/config` (`git remote add`, `git push -u`, `git switch --track`). Retry once the session has ended.

A file that can't be locked (on a filesystem without file flags, such as a network share, or a file owned by another user) gets a warning instead, and the session still starts. Don't replace or delete it on the host until the session ends; the end check below warns if its mount was dropped.

Before the sandbox starts, kota waits until the Docker VM sees every protected and masked path as the host does (type, size and modification time), checking from one offline container every 250ms. That covers a file replaced on the host just before the session, including by kota's own cleanup after the previous session. If they don't match within 10 seconds, the session refuses to start and names the paths that differ; `kota check` reports them and runs its probes anyway.

At the end, while the sandbox still runs, kota checks that every protection and mask mount is still in place, and warns for each one that is gone: for a mask, the agent may have read the secret; for a read-only path, it was writable for part of the session, so review it. This is a warning, and it doesn't change kota's exit status.

kota unlocks the files when the session ends, including after Ctrl-C, SIGTERM, a closed terminal or a startup error, before it removes placeholders and seeded files. A file you had locked yourself before the session is left locked. If kota is killed outright (SIGKILL) or crashes, the files stay locked until the next kota command on the project, which unlocks them before it does anything else. To unlock them by hand, run `chflags nouchg` on each file; kota's list of them is in `~/Library/Caches/kota/registry/*.locked-files`.

Limits:

- **Directories aren't locked**: masked directories (`.aws/`, `.ssh/`, your own `secrets/`), `.git/hooks` and the pinned directories on the way to protected files. Replacing a whole one on the host during a session (renaming another directory over it, or deleting and re-creating it) still drops its mount. The end check warns about it.
- **macOS only.** Linux isn't supported yet: it has no equivalent flag for the user, and a mount there is dropped at once when the file is replaced.
- **Checked on Rancher Desktop** (VZ, virtiofs) only. Docker Desktop, Colima and OrbStack aren't checked.

## Changed files that run code

Some files that run things can't be locked without breaking normal work, so kota reports them instead. Before `run`, `shell` or `prep`, it records them, and when the command ends it lists every one that was added, modified or deleted. They are, at any depth:

- manifests and build files: `package.json`, `go.mod`, `Makefile` (and `makefile`, `GNUmakefile`), `build.rs`, `justfile` (and `Justfile`, `.justfile`), `Taskfile.yml` and `Taskfile.yaml`, `*.nix`, `flake.lock` and the `.cargo/` directory;
- environment and tool versions: `.envrc`, mise's `mise.toml`, `.mise.toml`, `mise.local.toml` and `.mise.local.toml`, and `.tool-versions`;
- git and hooks: `.gitattributes`, `.gitmodules`, `.pre-commit-config.yaml`, and the `.husky/` and `.githooks/` directories;
- editors and CI: the `.vscode/`, `.idea/`, `.devcontainer/` and `.github/workflows/` directories;
- agent skills: the `.agents/skills/` directory at the project root, whatever `kota.yaml` says. Other agents you run on the host load it, and a session's agent does when the project loads its [Project skills](sessions.md#project-skills).

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
