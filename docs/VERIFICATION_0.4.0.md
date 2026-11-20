# Docker 0.4.0 verification · 2026-11-21

Implements issues #3 (original Compose management), #4 (container shortcuts/copy) and #5 (log wrapping). Based on main `84e168d`, independently of the connection-tree preview branch.

## Behavior and boundaries

Compose source metadata comes from `com.docker.compose.project.working_dir` and `com.docker.compose.project.config_files`. Containers in a project must agree on their source paths. Paths must be absolute. No arbitrary host-path or command RPC is exposed. Registry entries contain only paths and resource counts, keyed by a hash of the full connection identity; source text and `.env` contents are not persisted there. Retained metadata permits redeployment after `down`.

Native operations require local daemon access or the plugin's SSH socket transport and host Docker Compose v2+. Local CLI invocations use the same explicit daemon endpoint/TLS credentials, ignoring ambient Docker contexts and Compose environment variables. SSH commands reuse the verified connection, quote every argument, share the eight-session budget, and support cancellation including queued commands. Windows commands do not open console windows.

Environment files recorded in `com.docker.compose.project.environment_file` are passed explicitly in order; otherwise Compose uses the source directory's `.env`. Original shell-only variables and command-line-only profile selection cannot be recovered from Docker Engine metadata. Confirm those settings on the host before deployment; the plugin does not claim to replay unrecorded invocation flags.

Single-file edits preserve literal YAML and use atomic replacement. Multiple files remain read-only in the plugin and deploy in order. include/extends edits are rejected in the plugin; editing on the host and deployment of the existing files remain supported. Source bodies are UTF-8 and bounded to 128 KiB per file, up to eight files. CLI output is bounded; resolved configuration is hashed, never returned to AI. Native management remains workbench-only.

Every native operation and copy requires a backend preview. Tokens bind connection, arguments and resource snapshots, expire after five minutes and are consumed once. Native snapshots also bind source bytes and Compose's effective rendered config (including effective `.env` changes). Writes are checked again after acquiring the connection write lock. A concurrent external edit after the final check cannot be made mutually exclusive with other host programs; inspect state after unknown/partial failures. Deployment saves edits before running Compose; saved source is retained if deployment then fails. No automatic write replay or automatic rollback is claimed. `down` never requests volume removal; changed Compose content may itself alter mounts/resources as shown to the operator before confirmation.

Copy pins the original image, retains ordinary configuration, removes Compose labels, host published ports/shared mounts and elevated device/privilege settings, and never starts the copy. It does not copy writable-layer or mounted data. Image-declared volumes may create new anonymous volumes, never reuse source data.

## Checks

- `npm test`: 32 frontend tests, including project states, filtering and source paths; `npm run build`: Vue/TypeScript and production bundle.
- `go -C backend test -race ./...`, `go -C backend vet ./...`, `go -C backend/sdk test ./...`.
- Source/discovery tests: original text, registry retention, conflicting metadata, file size limits, atomic replacement, path validation. Copy tests: fresh state binding, replay rejection, no secret data in preview, source unchanged, independent copied settings.
- Real SSH protocol fixtures: host argument quoting, cancellation, shared session quota; existing SSH `nc` concurrency tests retained.
- `npm run package`: Windows x64 package plus artifact metadata; manifest/backend versions match 0.4.0.
- Compose snapshot regression ignores asynchronous sandbox/endpoint teardown after stop, while container identity, lifecycle timestamps, runtime configuration, network membership, aliases and static IP changes still invalidate the preview.

## Compose visual and interaction verification

The rebuilt Compose UI reuses the existing toolbar, compact resource table, detail header/tabs, overview cards, dialogs and CodeMirror editor. Container copying and log wrapping retain their accepted implementation. See [visual QA and screenshots](../design-qa.md).

Headless Chromium exercised the production bundle and real framed Go sidecar against a uniquely named, disposable two-service Docker project. Verified light/dark and 980×720 layouts, search/empty state, dirty-navigation cancellation, readable two-service deployment review, exact project-name removal confirmation, actual stop/start with inline task results, missing-file retry/recovery, navigation to existing container details and backend read-only disabling edits/deployment. No browser page errors. Deployment/removal were previewed and cancelled in this browser run; full execution of those actions is covered by the separate live regression below. This is browser automation with a minimal DBX bridge, not a claim of manual DBX desktop acceptance.

## Live Docker

`scripts/check-release-040.mjs` creates a uniquely named disposable two-service project using Alpine, a matching `.env`, comments, `depends_on` and a named data volume. It calls the packaged Go sidecar over the framed RPC protocol using an isolated plugin data directory. Windows uses the local named pipe; Linux uses the local socket.

Verified original source recognition, no side effects during preview, stale source and effective environment changes rejected, edited YAML atomically saved/deployed, start/stop/restart, removal with volume retention and redeployment from remembered metadata. Copy is confirmed stopped, without source mounts/Compose labels. Raw mutation bypass, replay and read-only requests are refused. Cleanup removes only its own test project, copy and test volume.

Run with a locally available test image:

```sh
go -C backend build -o ../dist/verify-sidecar .
node scripts/check-release-040.mjs dist/verify-sidecar alpine:3.22
```

Windows: use `.exe` for the sidecar. CI runs the same project regression on Linux in addition to the existing disposable-container mutation tests. No production containers are modified, no daemon-wide prune is executed.

Native local Windows behavior is exercised against Docker Engine 29.7.2. Actual remote-host Compose and DBX desktop/model-session end-to-end operation were not revalidated in this change. Other platform packages rely on CI/cross-platform release builds, not a claim of desktop manual testing on every platform.
