# Dependencies

Sessions are offline, so a project's dependencies are installed beforehand with `kota prep [dir]`. Run it once before the first session, and again whenever dependencies change. When the agent needs a new dependency, it asks you; add it to the manifest on the host and run `prep` again.

`prep` runs in the same image as a session, with the same masks and read-only files (except the project-root `.npmrc`, see [Private registries](#private-registries)), but on a normal network so registries are reachable, and without an agent, the forwarder or the gateway key. If a step fails, its output is followed by a line naming it, for example `kota: install step npm failed (exit 1): npm ci`.

## What it runs

`prep` detects what to run from the project root:

- `package.json`: pnpm, yarn or npm, chosen by the `packageManager` field, else by lockfile (`pnpm-lock.yaml`, `yarn.lock`, otherwise npm). pnpm and yarn come from corepack. `packageManager` must be a plain version (`pnpm@10.0.0`, optionally `+sha512.…`), not a URL. With a lockfile, the install is frozen (`pnpm install --offline --frozen-lockfile`, `yarn install --frozen-lockfile` or `--immutable`, `npm ci`).
- `go.mod`: `go mod download`, in every module directory, or once at the root with a `go.work`. Keep `go.sum` complete (`go mod tidy` on the host).

To run something else, put the command in `kota.yaml` at the project root:

```yaml
prep:
  command: pnpm install && pnpm build   # replaces the detected installs; run by sh in /workspace
  dirs: [.venv]                         # extra dependency directories (see below)
```

The detected fetches (see [Shared caches](#shared-caches)) still run before the command.

## Private registries

`prep` reads the `.npmrc` at the project root, so its registry settings and auth tokens apply to npm, pnpm and yarn 1. It is mounted read-only as it is, in both phases: install scripts can read it, including those of registry dependencies, but nothing in `prep` can change it.

- Only the root file counts. A `.npmrc` anywhere else in the project stays masked.
- kota never reads your host `~/.npmrc` or global npm config, and passes no host environment variables, so a `${NPM_TOKEN}` reference in the file is empty. Write the token into the project `.npmrc`, and keep that file out of git.
- Keep a token that `prep` doesn't need, such as one with publish rights, in your host `~/.npmrc`, not in the project.
- A root `.npmrc` that is a symlink, a directory or anything else but a regular file makes `prep` refuse before it asks, since the mount would follow the link to whatever host file it names.
- Sessions never see it: every `.npmrc` in the project is masked in `run`, `shell` and `check`, and `!.npmrc` in `.kotaignore` is ignored with a warning.
- yarn 2+ doesn't read `.npmrc`, and its `.yarnrc.yml` isn't masked: don't put `npmAuthToken` there, since the agent can read it.

## The confirmation

The agent can write the files that decide what `prep` runs, and `prep` runs them with network access. So `prep` asks you to type `y` first when:

- it is the project's first `prep`. It lists the commands, and the project code the install runs;
- the project has a root `.npmrc` that the last approved `prep` didn't read, including every project prepped before kota read it. While there is one, the confirmation says that `prep` and its install scripts can read it;
- a file that decides what `prep` runs changed since the last successful `prep`: `package.json` files, lockfiles, `pnpm-workspace.yaml`, `.pnpmfile.cjs`, `.npmrc`, `.yarnrc.yml`, `.yarn/releases/`, `.yarn/plugins/`, patches, `go.mod`, `go.sum`, `go.work` or `kota.yaml`;
- the install runs project code, and any other project file changed, since that code may run it. Project code is a lifecycle script (`postinstall`, `prepare`, ...), a pnpmfile, a `yarnPath` or the `kota.yaml` command. Lifecycle scripts count in the root and workspace packages, and in local dependencies (`file:`, `link:`, `portal:` or a relative path inside the project, and theirs in turn) listed in `dependencies`, `devDependencies` or `optionalDependencies`. A local tarball dependency and any git dependency (`github:user/repo`, `user/repo`, `git+https://...`) count too.

It lists what changed; review it (`git diff`) before you answer. Anything else, or no terminal, aborts. The state after each `prep` is recorded in `kota/prep/` under the user cache dir (`~/Library/Caches` on macOS), outside the sandbox. `kota.yaml` is read-only in the sandbox, so the agent can't change the command directly.

## Where dependencies live

Dependency directories (`node_modules` at the root and in each workspace package, plus `prep.dirs`) are Docker volumes per project, `kota-deps-<hash of the project path>-<hash of the directory>`, mounted over those paths in `prep`, `run`, `shell` and `check`. So Linux binaries never land in your project directory, and a `node_modules` you have on the host stays untouched and isn't visible in the sandbox. A dependency directory may not be, contain or lie inside a protected or masked path, and may not go through a symlink.

`prep` deletes and recreates these volumes each time, so nothing a session wrote into them runs during the install. It fails while a session of the project is running. `run` and `check` print a hint when a dependency directory is empty though its manifest exists.

If the dependencies can't be worked out, for example because the agent left `package.json`, `pnpm-workspace.yaml` or `kota.yaml` unparseable or set an unsupported `packageManager`, `prep` fails. `run`, `shell` and `check` warn and start anyway, so you can fix the file: they mount the dependency directories of the last successful `prep`, plus a volume over every other `node_modules` found, so no host `node_modules` is visible.

## Shared caches

Some caches are shared by all projects: the Go module cache (`kota-cache-gomod`), corepack's pnpm and yarn releases (`kota-cache-corepack`), and the pnpm store (`kota-cache-pnpm-store`). So that no project can plant code in another project's dependencies, only package manager code ever writes them. `prep` runs in two phases:

1. **Fetch**, with the caches writable: `corepack install`, `pnpm fetch --ignore-pnpmfile` and `go mod download`. No project code runs: no lifecycle scripts, pnpmfile or `yarnPath`. What is downloaded is checked against the lockfile, `go.sum` or the `packageManager` hash, and the caches are keyed by content or version.
2. **Install**, with the caches read-only: `pnpm install --offline`, `yarn install`, `npm ci` and the `kota.yaml` command, which run lifecycle scripts.

pnpm without a lockfile has nothing to fetch ahead, so its install uses a store of its own for that run. Sessions mount the Go module and corepack caches read-only, which the probes check, with `GOPROXY=off` and `COREPACK_ENABLE_NETWORK=0`, so a missing dependency fails at once. `kota clean --shared-caches` resets them (see [Cleanup](sessions.md#cleanup)).

## Limits

- **`prep` has the network**: the internet, the LAN and the host's loopback services, including the gateway port (but without the key). It runs no containment probes.
- **The confirmation is only as good as your review.** A dependency from the registry can still run its own install scripts, with the masks in place.
- **Changes in dependency and build directories** (`dist`, `build`, `vendor`, ...) don't count for the confirmation. Don't let install scripts run files from there.
- **Install scripts can't change `.git/config`**, so a `prepare` script that sets `core.hooksPath` (husky) fails or does nothing; run it on the host.
- **Install scripts can read the root `.npmrc`**, tokens included, and send them anywhere. Keep only the tokens the install needs there (see [Private registries](#private-registries)).
- `prep` reports changed files that run code, and commits, like `run` (see [Protecting the host](protections.md)).

## Project image

A project that needs more than Node and Go adds a `kota.Dockerfile` at its root, on top of the base image:

```dockerfile
FROM kota-base:latest
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*
```

`kota build dir` builds it as `kota-project-<hash of the project path>`, and `prep`, `run`, `shell` and `check` of that project then use it (`check` shows the image). Projects without the file use the base image.

An `ENV` line, such as `ENV APP_ENV=test`, sets that variable in all four. The file is readable in the sandbox and usually committed, so use it for settings, not secrets. Where kota sets the same variable, such as `HOME` or `KOTA_GATEWAY_KEY`, kota's value wins.

The build runs as root with network access, like any `docker build`, but with an empty build context, so `COPY` and `ADD` can't read the project or its masked secrets. Download what you need in a `RUN`, or copy it from another image (`COPY --from=...`). The file is read-only in the sandbox, so the agent can't change what the next build runs, and a symlinked `kota.Dockerfile` is refused. The sandbox sets the user, capabilities and networks, so `USER` or `EXPOSE` in the Dockerfile don't change them.

When the Dockerfile has changed, or `kota build` has rebuilt the base image since, sessions warn and use the old image; without an image yet, they warn and use the base image. Each warning shows the `kota build dir` that fixes it.

### Rust

Copy the toolchain from the official image, the way the base image gets Go, and keep Cargo's downloads and build output in dependency volumes:

```dockerfile
FROM rust:1-slim-trixie AS rust

FROM kota-base:latest
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo
# The toolchain stays in the image; the registry and git checkouts go in
# the .cargo-home dependency volume.
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/workspace/.cargo-home \
    PATH=/usr/local/cargo/bin:$PATH
```

```yaml
# kota.yaml
prep:
  command: cargo fetch
  dirs: [.cargo-home, target]
```

- Pin the `rust` tag to a version for reproducible builds, and add `.cargo-home/` and `target/` to `.gitignore`.
- `cargo fetch` needs a `Cargo.toml`. Without a `Cargo.lock` it writes one, so commit the lockfile after `prep`. The sandbox can't create one, so an agent that adds the first crates gets no dependencies until the next `prep`.
- In sessions, `cargo build` and `cargo test` then work offline; add `--offline` if Cargo tries the network anyway.

To make Rust available to every project instead, add the same `COPY` and `ENV` lines to `assets/images/base/Dockerfile`, and, for a registry cache shared across projects like the Go module cache, a `CARGO_HOME` entry in `sandbox.sharedCaches` with `cargo fetch` in the fetch phase.
