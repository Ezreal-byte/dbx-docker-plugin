# Docker for DBX

`io.dbx.docker` adds a Docker Engine connection and workbench to DBX. The backend is a Go sidecar using the Docker SDK; the UI is a Vue workbench packaged into a `.dbxp` file.

The workbench lists containers, images, volumes, and networks. It supports container lifecycle and creation, image pull/push/export, live logs, short-term resource monitoring, read-only file browsing, an interactive container terminal, and a controlled subset of Compose YAML. It was adapted from the unmerged DBX Docker workbench branch. The source branch remains available for comparison at [`Ezreal-byte/dbx` (`codex/feature-docker-workbench`)](https://github.com/Ezreal-byte/dbx/tree/codex/feature-docker-workbench).

The UI follows the DBX interface language. It reads `dbxPlugin.locale` after the host initialization handshake and re-applies the language on `dbx-plugin-init` / `dbx-plugin-env`, so switching the DBX language updates the workbench without a reload.

## Requirements and connection security

- DBX 0.6.14 or newer, Host API 1.
- An accessible Docker Engine API. Local Docker Desktop TCP normally uses `127.0.0.1:2375`; HTTPS normally uses port 2376. Unix sockets and Unix sockets reached through SSH `nc -U` are also supported.
- Plain HTTP to a remote host is rejected unless **Allow insecure remote HTTP** is explicitly enabled. An exposed Docker daemon grants control comparable to the host user or root account. Prefer HTTPS with certificates or an SSH tunnel.
- SSH `nc` connections verify the server key against `~/.ssh/known_hosts` by default. Set **SSH known_hosts path** if the trusted file is elsewhere.
- Read-only connections reject Docker write operations and container terminals in the Go backend.

## DBX tunnel reuse

For TCP connections (`http` / `https`) the plugin does not build its own SSH tunnel. When the DBX connection carries an enabled transport layer, DBX resolves the tunnel and delivers the final endpoint in `runtime.host` / `runtime.port`; the plugin dials that endpoint while keeping the configured host in the HTTP `Host` header and TLS SNI. Plain HTTP to a non-loopback host is accepted **because** the tunnel is active, not because the insecure-HTTP switch was flipped.

Unix sockets cannot be expressed as a DBX static TCP forward, so `unix-over-nc` and `unix-over-nc-sudo` remain the plugin's own SSH `nc -U` bridge (with `known_hosts` verification) for that case only.

## Container terminal

Running containers expose a **Terminal** tab backed by `docker exec` with a TTY. The frontend renders an xterm.js terminal and streams keystrokes to the sidecar over the `docker-exec` binary channel; output returns on the same channel. The default command is `/bin/sh` and can be replaced with any argv (for example `/bin/bash` or `psql -U postgres`). Terminals are refused on read-only connections, and on connections marked as production they require an explicit confirmation first.

The plugin does not include a Docker daemon or the Docker CLI. The Compose editor supports common image, environment, port, volume, network, restart, label, and command fields. It does not implement builds, `.env`, `depends_on` health checks, or arbitrary Compose extensions. File browsing requires `/bin/sh`, `stat`, and `head` in the container, and the container terminal requires the requested command to exist inside the container (distroless images without a shell cannot open one). Image export is buffered in the workbench, so very large archives may exceed available memory.

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

The end-to-end smoke test drives the built sidecar over the framed protocol and needs a reachable Docker Engine:

```powershell
go -C backend build -o ../dist/verify-sidecar.exe .
node scripts/smoke-sidecar.mjs dist/verify-sidecar.exe http://127.0.0.1:2375 <running container name>
```

It verifies the handshake, direct and tunnel-routed connections, the remote plain-HTTP guard, resource listing, log streaming, read-only file browsing, the interactive terminal (stdin, resize, exit), and the read-only terminal guard.

On Windows x64, the package is `dist/io.dbx.docker-0.1.2-windows-x64.dbxp`. It is an **unsigned review candidate**. To test it in DBX, enable **Allow unsigned development package** in Plugin Center, then install the `.dbxp` locally. Normal marketplace installation requires DBX Store review and signing.

Publishing a GitHub Release triggers `.github/workflows/release.yml` to build separate native candidates for Windows, macOS, and Linux. Release assets and `release-candidates.json` are the inputs to a candidate PR in [`t8y2/dbx-store`](https://github.com/t8y2/dbx-store). The store signs approved bytes; this repository contains no signing key.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
