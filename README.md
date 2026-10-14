# Docker for DBX

`io.dbx.docker` adds a Docker Engine connection and workbench to DBX, with built-in DBX AI and external MCP tools for queries and permission-controlled management. The backend is a Go sidecar using the Docker SDK; the UI is a Vue workbench packaged into a `.dbxp` file.

The workbench lists containers, images, volumes, and networks. It supports container lifecycle and creation, container rename, image pull/push/export, image tagging and per-tag removal, image layer history, live logs, container resource monitoring, container file browsing with upload and download, an interactive container terminal, engine disk usage with pruning, and a controlled subset of Compose YAML. It was adapted from the unmerged DBX Docker workbench branch. The source branch remains available for comparison at [`Ezreal-byte/dbx` (`codex/feature-docker-workbench`)](https://github.com/Ezreal-byte/dbx/tree/codex/feature-docker-workbench).

The UI follows the DBX interface language. It reads `dbxPlugin.locale` after the host initialization handshake and re-applies the language on `dbx-plugin-init` / `dbx-plugin-env`, so switching the DBX language updates the workbench without a reload.

## Built-in AI and MCP operations

- **DBX built-in AI:** open a Docker connection, enable its **AI/MCP permissions**, enable the plugin's AI tools in DBX, and select **Agent** mode. Ask it to list containers, analyze health and bounded logs, or explain which containers use a network or volume.
- **External MCP:** connect your client to **DBX MCP**, authorize the Docker connection and tools in DBX, and select the saved connection through `dbx_connection`. The plugin reuses DBX MCP; no separate MCP service or port is needed. Reconnect the client after installation or permission changes to refresh its tool list.
- **Controlled management:** separately authorize container lifecycle actions, image pulls/tags, network attachment, resource creation, Compose deployment, deletion and cleanup. Permissions are off by default and isolated by connection. Both AI entry points share backend checks; DBX read-only restrictions, allowlists and tool approvals still apply. Destructive tools require a bound preview before execution.
- Example prompts: “列出正在运行的容器”, “分析这个容器退出的原因”, “这个卷被哪些容器使用？” and, after granting the relevant permissions, “重启测试容器，操作前让我确认” or “预览清理未使用的网络”. Interactive terminals and file writes remain workbench-only.
- [AI/MCP 使用、权限与工具说明（中文）](docs/AI-MCP.md)

## 0.3.0 — 2026-10-14

- Larger, centered Docker icon and updated descriptions for built-in AI / external MCP operations.
- Today's workbench improvements include theme-aware Compose group rows, compact tables, clearable search, highlighted port shortcuts, YAML Compose editing, refreshed overview/monitoring, container file menus/previews and unified transfer tasks. Column resizing was removed following feedback.
- Destructive workbench RPCs now enforce bound previews, including legacy deletion endpoints, tag replacement and network disconnect. Read-only flags use the current host lifecycle.
- File uploads require a backend preview; replacing a file asks for confirmation. Uploads stage data and verify the original target before committing, preserving it on interruption.
- Confirmation dialogs stay above editors, keep keyboard focus inside, and cancel on Escape. Pause and image push also require confirmation.
- Push and pull-request CI verifies Windows/Linux, runs a disposable-container safety check on Linux, and saves the Windows package as an Actions artifact.
- [完整更新日志（中文）](CHANGELOG.md) · [0.3.0 safety audit and verification](docs/0.3.0-audit.md)

### Previous 0.2.3 changes

- Compose groups use the theme's muted background, while child containers use the normal background. Column resizing and stored-width preferences have been removed from all four resource tables.
- File rename/create dialogs submit directly through the plugin bridge, including Enter, without relying on native form submission blocked by the DBX sandbox. Monitoring no longer shows the redundant heading and subtitle.
- Interactive sh/bash/ash sessions enable colored prompts and `ls` output without modifying container startup files. Other output colors still depend on the container program.
- [0.2.3 changes and focused validation](docs/0.2.3-validation.md)

### Previous 0.2.2 changes

- Published TCP ports are highlighted browser shortcuts.
- Compose and file previews share a CodeMirror editor with YAML, JSON, XML, properties/INI, TOML, Nginx, shell and Dockerfile highlighting. Common raster images have inline previews.
- Container overview has grouped runtime, network and mount information. Monitoring follows Overview and uses refreshed cards/charts. File uploads and downloads appear in the header transfer list and remain visible after navigating between containers.
- [0.2.2 changes, limits and validation](docs/0.2.2-validation.md)

### Previous 0.2.1 changes

- Resource names use normal text colors; compact tables distinguish Compose groups from child containers, and remote loading uses skeletons. Search fields can be cleared.
- Compose editing returns YAML with exact external volume/network references and preserves supported runtime settings. Unsupported settings are rejected before replacement.
- Container files have a context menu for opening, downloading (folders as `.tar`), uploading, creating, renaming and deleting. The xterm.js terminal supports ANSI colors and search highlighting.
- The AI/MCP button has an AI icon; the three engine information dialogs have close buttons. See the [0.2.1 changes and verification record](docs/0.2.1-validation.md).

- **AI/MCP permissions** in the header are disabled by default for every connection. Enabling starts with queries only; individual change/create/delete tools require a separate save.
- DBX built-in AI and external DBX MCP use the same backend authorization. There is no independent MCP server. Host read-only, tool approval and connection allowlists still apply.
- Image, volume and network names open inspect details and container references. Network connections can be attached/detached, and protected resources cannot be deleted.
- Compose deployments, cleanup and image pulls support task status/cancellation; destructive work uses a bound preview. Existing UI streams remain available.
- [权限、工具及连接说明（中文）](docs/AI-MCP.md) · [验证记录](docs/0.2.0-validation.md) · [二、三期路线](docs/ROADMAP.md)

## Requirements and connection security

- DBX 0.6.43 or newer, Host API 1. Version 0.2.0 adds DBX AI/MCP, per-connection permissions and Named Pipe support.
- An accessible Docker Engine API. Local Docker Desktop TCP normally uses `127.0.0.1:2375`; HTTPS normally uses port 2376. Unix sockets and Unix sockets reached through SSH `nc -U` are also supported.
- Plain HTTP to a remote host is rejected unless **Allow insecure remote HTTP** is explicitly enabled. An exposed Docker daemon grants control comparable to the host user or root account. Prefer HTTPS with certificates or an SSH tunnel.
- SSH `nc` connections verify the server key against `~/.ssh/known_hosts` by default. Set **SSH known_hosts path** if the trusted file is elsewhere. The handshake tries host key types in OpenSSH's order (ed25519, then RSA, then ECDSA), so an entry written by `ssh` or `ssh-keyscan` matches even when the server also offers other key types. A verification failure names the file and the `ssh-keyscan` command that refreshes it.
- Read-only connections reject Docker write operations, container terminals, and container file uploads in the Go backend.
- Every RPC handler is wrapped in a panic guard, so a failure inside one method returns an RPC error instead of taking the whole sidecar (and the open workbench) down.

## DBX tunnel reuse

The connection form only shows fields for the selected **Connection method**:

| Method | Connection fields | DBX Tunnel / Proxy |
| --- | --- | --- |
| HTTP | Docker host, port (usually 2375), insecure remote HTTP option | Supported |
| HTTPS | Docker host, port (usually 2376), optional CA and client certificate/key | Supported |
| Unix socket (local) | Local socket path | Leave empty |
| Windows Named Pipe (local) | `//./pipe/docker_engine` (Windows only) | Leave empty |
| SSH (Unix socket) | SSH host/port/user, password or key, known_hosts, remote socket path | Leave empty; the plugin opens SSH itself |
| SSH (Unix socket, sudo) | Same SSH fields; remote `sudo -n nc -U` needs passwordless sudo | Leave empty; the plugin opens SSH itself |

Connection name, API version, read-only and production settings remain available for all methods. Certificate and SSH key fields have desktop file pickers. Switching methods preserves saved values but hides unrelated fields. Existing connection keys and protocol values remain compatible.

For a remote daemon listening on `127.0.0.1:2375`, choose **HTTP**, enter that address, then add the SSH hop in **Tunnel / Proxy**. For a remote daemon with no TCP listener, choose **SSH (Unix socket)**, enter SSH credentials and `/var/run/docker.sock`, and leave **Tunnel / Proxy** empty.

DBX currently exposes the Tunnel / Proxy tab for every plugin protocol; a plugin manifest cannot hide that tab conditionally. Combining it with a socket method is rejected instead of silently ignoring the configured route. DBX also validates the bound Docker port even when hidden, so new socket connections retain a harmless default port internally. If an older socket connection has a stored port of 0, temporarily choose HTTP and set 2375, then switch back to the socket method and save.

For TCP connections (`http` / `https`) the plugin does not build its own SSH tunnel. DBX resolves the transport layers and delivers the final endpoint in `runtime.host` / `runtime.port`; the plugin dials that endpoint while keeping the configured host in the HTTP `Host` header and TLS SNI. An enabled transport layer with a missing or invalid endpoint fails before dialing, without falling back to a direct connection. Plain HTTP to a non-loopback host is accepted when the tunnel is active without enabling insecure remote HTTP.

Unix sockets cannot be expressed as a DBX static TCP forward, so `unix-over-nc` and `unix-over-nc-sudo` remain the plugin's own SSH `nc -U` bridge (with `known_hosts` verification) for that case only.

### When the remote sshd refuses TCP forwarding

Some appliances (fnOS, Synology-style NAS images, bastion hosts such as JumpServer/Koko) ship `AllowTcpForwarding no` either directly in `sshd_config` or through an `sshd_config.d/` drop-in. DBX opens its tunnel with `direct-tcpip`, so sshd rejects the channel as **Administratively prohibited**, and the tunnel's local listener drops the connection immediately. The plugin only sees a reset:

```
Failed to reach Docker daemon: error during connect: Head "http://127.0.0.1:2375/_ping":
read tcp 127.0.0.1:56634->127.0.0.1:16657: An existing connection was forcibly closed by the remote host.
```

The endpoint printed there is the tunnel's local port, not the port you configured, and `ss -lnt` on the remote still shows the daemon listening, so this is easy to misread as a plugin bug. Reproduce it with any client to confirm the diagnosis:

```bash
ssh -L 12375:127.0.0.1:2375 user@host -N     # then: curl http://127.0.0.1:12375/_ping
# plink -v reports: Forwarded connection refused by remote: Administratively prohibited [open failed]
ssh user@host 'grep -ri AllowTcpForwarding /etc/ssh/sshd_config /etc/ssh/sshd_config.d/'
```

Two workarounds, neither of which needs sshd to be relaxed:

1. In DBX, enable **exec channel proxy** for the SSH hop. DBX then falls back to `nc <host> <port>` over an SSH exec channel, which `AllowTcpForwarding` does not govern.
2. Switch the connection to the **`unix socket over SSH (nc -U)`** protocol with socket path `/var/run/docker.sock`. It uses an exec channel only, so the forwarding policy is irrelevant. This requires `nc` with `-U` support on the remote (OpenBSD netcat or BusyBox) and read access to the socket — being in the `docker` group is enough.

## Container terminal

Running containers expose a **Terminal** tab backed by `docker exec` with a TTY. The frontend renders an xterm.js terminal and streams keystrokes to the sidecar over the `docker-exec` binary channel; output returns on the same channel. The default command is `/bin/sh` and can be replaced with any argv (for example `/bin/bash` or `psql -U postgres`). Terminals are refused on read-only connections, and on connections marked as production they require an explicit confirmation first.

## Monitoring charts

Container monitoring renders with ECharts (canvas), not a scaled SVG, in a fixed 2x2 grid — CPU % and memory on the first row, network throughput and block I/O on the second. Network and block I/O are derived from Docker's cumulative counters with per-second rates, and counter resets (a container restart) are clamped to zero instead of producing a negative spike. A summary row shows the latest sample in numbers.

## Files in a container

The **Files** tab supports browsing, image/config previews, a context menu for downloads (folders as `.tar`), uploads, file/folder creation, rename and deletion. Uploads and downloads share the header's transfer list. Downloads stream over the `docker-file` binary channel and use the native save dialog; uploads require a backend preview, stage data in the destination directory, verify the byte count and target state, then commit. Overwrites ask for confirmation and interrupted uploads preserve the original file. Both directions are capped at 256 MiB and require `/bin/sh` plus the file commands used by the operation, including `stat`, `head`, `mktemp`, `ln` and `mv` for uploads.

## Image export and where files land

Image export asks the desktop host for a native save dialog and the host returns the absolute path it wrote. Every finished download keeps that path in the transfer record, so the **Transfers** popover shows the destination directory and a one-click **copy path** button instead of leaving the file location unknown. The web host cannot hand a client path to the plugin, so there the record shows the browser-download hint instead of a directory.

A plugin sandbox has no host API to reveal a path in the OS file manager (`reveal_path_in_file_manager` is app-internal), so the plugin copies the path rather than opening Explorer.

## Disk usage and cleanup

The header opens a `docker system df` view with per-category size and reclaimable space for images, containers, volumes, build cache, and networks.

Cleanup is deliberately hard to trigger by accident:

1. It lives in a **modal dialog**, not a one-click button.
2. Picking a category runs a preview through `docker/mutation`, listing the exact candidate IDs and issuing a single-use token valid for five minutes. Read-only connections can still use `docker/prunePreview`.
3. The delete button stays disabled until the user **types the confirmation phrase** (`我同意` / `I AGREE`) exactly.
4. Execution rechecks target state and only deletes the bound candidates. The task result reports deleted, failed and unprocessed resources; it never adds newly eligible resources.

Targets mirror the Docker CLI defaults instead of "delete as much as possible":

| Target | Removes | Keeps |
| --- | --- | --- |
| Stopped containers | every container not running or paused | running / paused containers |
| Dangling images | untagged images no container references | tagged images |
| Unused images (`all`) | every image no container references, tagged or not | images in use |
| Unused anonymous volumes | same as `docker volume prune` | named volumes |
| Unused volumes (`all`) | every volume no container uses, incl. named | volumes in use |
| Unused networks | networks with no container attached | predefined Docker networks |

The preview needs the same capabilities as the operation itself (`/bin/sh` is not required; it only uses the Docker API). A destructive action still requires a writable connection.

## Limitations

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

It verifies the handshake, direct and tunnel-routed connections, the remote plain-HTTP guard, resource listing, log streaming, read-only file browsing, the interactive terminal (stdin, resize, exit), a container file upload/download byte-for-byte round-trip, the read-only terminal/upload guards, disk usage, image tag/untag round-trip, image layer history, and container rename round-trip.

The container rename and image tag checks restore the original state in a `finally` block, and the file round-trip removes its temporary file. Prune checks only inspect candidates and verify rejection without a token; the smoke test never executes daemon-wide cleanup.

On Windows x64, the package is `dist/io.dbx.docker-0.3.0-windows-x64.dbxp`. It is an **unsigned review candidate**. To test it in DBX, enable **Allow unsigned development package** in Plugin Center, then install the `.dbxp` locally. Normal marketplace installation requires DBX Store review and signing.

To check form visibility, localization, required fields, secret storage and serialization against an upstream checkout:

```powershell
node scripts/check-dbx-connection-contract.mjs ../dbx upstream/main
```

The connection audit was checked against DBX 0.6.43 (`0db01f7f284ec6b0b090fe7369de7d05c37b25b0`). The script loads the real upstream evaluator and serializer without changing the DBX checkout.

Version 0.3.0 completes the Windows verification and safety audit stage. The [GitHub Release](https://github.com/Ezreal-byte/dbx-docker-plugin/releases/tag/v0.3.0) supplies platform candidates; these remain unsigned until DBX Store review and signing.

Publishing a GitHub Release triggers `.github/workflows/release.yml` to build separate native candidates for Windows, macOS, and Linux. Release assets and `release-candidates.json` are the inputs to a candidate PR in [`t8y2/dbx-store`](https://github.com/t8y2/dbx-store). The store signs approved bytes; this repository contains no signing key.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
