# Docker for DBX

`io.dbx.docker` adds a Docker Engine connection and workbench to DBX. The backend is a Go sidecar using the Docker SDK; the UI is a Vue workbench packaged into a `.dbxp` file.

The workbench lists containers, images, volumes, and networks. It supports container lifecycle and creation, image pull/push/export, live logs, short-term resource monitoring, read-only file browsing, and a controlled subset of Compose YAML. It was adapted from the unmerged DBX Docker workbench branch. The source branch remains available for comparison at [`Ezreal-byte/dbx` (`codex/feature-docker-workbench`)](https://github.com/Ezreal-byte/dbx/tree/codex/feature-docker-workbench).

## Requirements and connection security

- DBX 0.6.14 or newer, Host API 1.
- An accessible Docker Engine API. Local Docker Desktop TCP normally uses `127.0.0.1:2375`; HTTPS normally uses port 2376. Unix sockets and Unix sockets reached through SSH `nc -U` are also supported.
- Plain HTTP to a remote host is rejected unless **Allow insecure remote HTTP** is explicitly enabled. An exposed Docker daemon grants control comparable to the host user or root account. Prefer HTTPS with certificates or an SSH tunnel.
- SSH `nc` connections verify the server key against `~/.ssh/known_hosts` by default. Set **SSH known_hosts path** if the trusted file is elsewhere.
- Read-only connections reject Docker write operations in the Go backend.

The plugin does not include a Docker daemon or the Docker CLI. The Compose editor supports common image, environment, port, volume, network, restart, label, and command fields. It does not implement builds, `.env`, `depends_on` health checks, arbitrary Compose extensions, or an interactive terminal. File browsing requires `/bin/sh`, `stat`, and `head` in the container. Image export is buffered in the workbench, so very large archives may exceed available memory.

## Build and local verification

Requires Node.js 20 or newer and Go 1.26 or newer.

```powershell
npm ci --ignore-scripts
npm test
go -C backend test ./...
go -C backend/sdk test ./...
go -C backend vet ./...
npm run package
```

On Windows x64, the package is `dist/io.dbx.docker-0.1.0-windows-x64.dbxp`. It is an **unsigned review candidate**. To test it in DBX, enable **Allow unsigned development package** in Plugin Center, then install the `.dbxp` locally. Normal marketplace installation requires DBX Store review and signing.

Publishing a GitHub Release triggers `.github/workflows/release.yml` to build separate native candidates for Windows, macOS, and Linux. Release assets and `release-candidates.json` are the inputs to a candidate PR in [`t8y2/dbx-store`](https://github.com/t8y2/dbx-store). The store signs approved bytes; this repository contains no signing key.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
