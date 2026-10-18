# Migrating from localllm

kota was called localllm. It starts fresh: it doesn't read localllm's volumes, images, data dir, project files or environment variables. To switch:

1. Before installing kota, run `localllm clean [dir]` in each project you used it in, and `localllm clean --shared-caches [dir]` once to remove the shared caches.
2. Rename the project files: `localllm.yaml` to `kota.yaml`, `.localllmignore` to `.kotaignore` and `localllm.Dockerfile` to `kota.Dockerfile`. kota ignores the old names, so a project that still has only them runs without its configuration, masks or image.
3. Rename the environment variables: `LOCALLLM_GATEWAY`, `LOCALLLM_GATEWAY_KEY`, `LOCALLLM_GATEWAY_KEY_FILE` and `LOCALLLM_ALLOWED_ROOT` become `KOTA_*`.
4. Remove what's left: the old images (`docker rmi localllm-base localllm-forwarder` and the project images, `docker images 'localllm-project-*'`), the old data dir (`localllm/` in the user cache dir, `~/Library/Caches/localllm` on macOS, including the Engine base disk) and the `localllm` binary (`rm "$(go env GOPATH)/bin/localllm"`).
5. Install kota and run `kota build`, then `kota prep` in each project.
