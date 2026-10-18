# Dependencies

`run` is offline, so dependencies are installed beforehand with `kota prep [dir]`. It runs in the same image, with the same masks and read-only `.git/hooks`, `.git/config`, `.envrc` and `kota.yaml` as `run`, but on a normal network so registries are reachable, and without an agent, the forwarder or the gateway key. Run it again after changing a manifest. When the agent needs a new dependency, add it to the manifest on the host and re-run `prep`. If a step fails, its output is followed by a line naming the step, its command and exit status, e.g. `kota: install step npm failed (exit 1): npm ci`.

What it runs is detected from the project root:
- `package.json`: pnpm, yarn or npm, chosen by the `packageManager` field, else by lockfile (`pnpm-lock.yaml`, `yarn.lock`, otherwise npm). pnpm and yarn come from corepack. `packageManager` must be a plain version (`pnpm@10.0.0`, optionally `+sha512.…`), not a URL. With a lockfile, the install is frozen (`pnpm install --offline --frozen-lockfile`, `yarn install --frozen-lockfile` or `--immutable`, `npm ci`).
- `go.mod`: `go mod download`, in every module directory, or once at the root with a `go.work`. Keep `go.sum` complete (`go mod tidy` on the host).

To run something else, put the command in `kota.yaml` at the project root:

```yaml
prep:
  command: pnpm install && pnpm build   # replaces the detected installs; run by sh in /workspace
  dirs: [.venv]                         # extra dependency directories (see below)
```

The command runs with network access, so `kota.yaml` is read-only inside the sandbox, like `.kotaignore`. The detected fetches (below) still run before it.

Dependency directories (`node_modules` at the root and in each workspace package, plus `prep.dirs`) are Docker volumes per project, `kota-deps-<hash of the project path>-<hash of the directory>`, mounted over those paths in `prep`, `run` and `check`. Linux binaries never land in your project directory. A `node_modules` you have on the host stays untouched and isn't visible in the sandbox. A dependency directory may not be or contain a protected or masked path, or lie inside one, and may not go through a symlink. `prep` deletes and recreates these volumes each time, so nothing an agent session wrote into them runs during the install. It fails while a session of the project is running.

If the dependencies can't be worked out, for example because an agent left `package.json`, `pnpm-workspace.yaml` or `kota.yaml` unparseable or set an unsupported `packageManager`, `prep` fails, but `run`, `shell` and `check` warn and start anyway so you can fix the file. They mount the dependency directories of the last successful `prep` (only the root `node_modules` if the project was never prepped), plus a volume over every other `node_modules` found on the host, so no host `node_modules` is visible in the sandbox. Run `prep` once the file is fixed.

Caches are shared by all projects: the Go module cache (`kota-cache-gomod`, `GOMODCACHE`), corepack's pnpm and yarn releases (`kota-cache-corepack`), and the pnpm store (`kota-cache-pnpm-store`). So that no project can plant code in another project's dependencies, only package manager code ever writes them. `prep` runs in two phases:
1. Fetch, with the caches writable: `corepack install`, `pnpm fetch --ignore-pnpmfile` and `go mod download`. No project code runs: no lifecycle scripts, pnpmfile or `yarnPath`. What is downloaded is checked against the lockfile, `go.sum` or the `packageManager` hash, and the caches are keyed by content or version.
2. Install, with the caches read-only: `pnpm install --offline`, `yarn install`, `npm ci` and the `kota.yaml` command, which run lifecycle scripts.

pnpm without a lockfile has nothing to fetch ahead, so its install uses a store of its own for that run; the next `prep` then uses the shared one. `run` mounts the Go module and corepack caches read-only, and sets `GOPROXY=off` and `COREPACK_ENABLE_NETWORK=0` so a missing dependency fails at once. `run` and `check` print a hint when a dependency directory is empty though its manifest exists.

The agent can write the files that decide what `prep` runs, and `prep` runs them with network access. So `prep` asks you to type `y` first when:
- it is the project's first `prep` (it lists the commands and the project code the install runs);
- a file that decides what prep runs changed since the last successful `prep`: `package.json` files, lockfiles, `pnpm-workspace.yaml`, `.pnpmfile.cjs`, `.npmrc`, `.yarnrc.yml`, `.yarn/releases/`, `.yarn/plugins/`, patches, `go.mod`, `go.sum`, `go.work` or `kota.yaml`;
- the install runs project code (lifecycle scripts like `postinstall` or `prepare`, a pnpmfile, a `yarnPath`, the `kota.yaml` command) and any other project file changed, since that code may run it. Lifecycle scripts count in the root and workspace packages, and in local dependencies (`file:`, `link:`, `portal:` or a relative path inside the project, and theirs in turn) listed in `dependencies`, `devDependencies` or `optionalDependencies`. A local tarball dependency and any git dependency (`github:user/repo`, `user/repo`, `git+https://...`) count too.

It lists what changed; review it (`git diff`) before you answer. Anything else, or no terminal, aborts. The state after each `prep` is recorded in `~/Library/Caches/kota/prep/` (the user cache dir), outside the sandbox.

Limits:
- `prep` has the same network as any container: the internet, the LAN and the host's loopback services (including the gateway port, but without the key). It runs no containment probes.
- Changes in dependency and build directories (`dist`, `build`, `vendor`, ...) don't count for the confirmation, as for the change report. Don't let install scripts run files from there.
- The confirmation is only as good as your review. A dependency from the registry can still run its own install scripts during its project's `prep`, with the masks in place.
- Install scripts can't change `.git/config`, so a `prepare` script that sets `core.hooksPath` (husky) fails or does nothing; run it on the host.
- `.npmrc` is masked by default, so registry settings and tokens in it don't apply. Unmask it in `.kotaignore` (`!.npmrc`) if `prep` needs it; the agent can then read it too.
- `prep` reports changed files that run things, like `run`.

## Project image

A project that needs more than Node and Go can add a `kota.Dockerfile` at its root, on top of the base image:

```dockerfile
FROM kota-base:latest
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*
```

`kota build dir` builds it as `kota-project-<hash of the project path>`, and `prep`, `run`, `shell` and `check` of that project then use it (`check` shows the image). Projects without the file use the base image. The build runs as root with network access, like any `docker build`, but with an empty build context, so `COPY` and `ADD` can't read the project or its masked secrets. Download what you need in a `RUN`, or copy it from another image (`COPY --from=...`). The file is read-only inside the sandbox, like `kota.yaml`, so the agent can't change what the next build runs. A symlinked `kota.Dockerfile` is refused.

The image is labeled with the Dockerfile's hash and the base image it was built on. When the Dockerfile has changed or `kota build` has rebuilt the base image since, sessions warn and use the old image. Without an image yet, they warn and use the base image. Each warning shows the `kota build dir` command that fixes it. Run it again to pick up a new base image even when the Dockerfile is unchanged. The sandbox sets the user, capabilities and networks, so `USER` or `EXPOSE` in the Dockerfile don't change them. The root container that prepares dependency volumes always uses the base image.

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

Pin the `rust` tag to a version for reproducible builds. Add `.cargo-home/` and `target/` to `.gitignore`. `cargo fetch` needs a `Cargo.toml`. Without a `Cargo.lock` it resolves the dependencies and writes one, so commit the lockfile after `prep`. Don't skip the fetch when the lockfile is missing: the sandbox can't create one, so an agent that adds the first crates gets no dependencies until the next `prep`. Run `prep` again whenever dependencies change. In `run`, `cargo build` and `cargo test` then work offline; add `--offline` if Cargo tries the network anyway.

To make Rust available to every project instead, add the same `COPY` and `ENV` lines to `assets/images/base/Dockerfile`, and, for a registry cache shared across projects like the Go module cache, a `CARGO_HOME` entry in `sandbox.sharedCaches` with `cargo fetch` in the fetch phase.
