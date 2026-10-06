# Codex Five-Hour Quota Warmer

`codex-5h-quota-warmer` is a Go dynamic-library plugin for CLIProxyAPI. It sends one non-streaming OpenAI-compatible message through every currently active Codex credential at configured daily times. Each request uses the exact credential ID returned by `host.auth.list`.

The plugin only selects credentials where all of the following are true:

- `provider` is `codex`;
- `disabled` is `false`;
- `id` is present.

The plugin reports `status` and `unavailable` in the selection log but does not use them to skip an enabled credential. CPA evaluates cooldown and availability for the configured warm-up model when the request is sent.

## Management UI

The plugin registers a browser resource at:

```text
/v0/resource/plugins/codex-5h-quota-warmer/status
```

The page provides a manual warm-up button, editable daily schedule rows, the next scheduled execution time for every enabled row, the latest credential-level results, and a bounded persistent log. It automatically loads these data when a previously saved Management key is available. Before the first run, the latest execution time is shown as unavailable.

The resource page carries no management data. Enter a CPA Management key in the page before it requests protected APIs. The optional browser storage checkbox stores that key in browser storage; avoid enabling it on shared devices.

The page writes settings through CLIProxyAPI's standard plugin configuration endpoint. This preserves the configured `enabled` and `priority` values while updating the plugin-owned settings.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-5h-quota-warmer:
      enabled: true
      priority: 1
      model: "gpt-5.6-luna"
      prompt: "Reply with exactly: quota window activated"
      timezone: "Asia/Singapore"
      max_log_entries: 200
      state_file: "plugins/state/codex-5h-quota-warmer.json"
      schedules:
        - id: "morning-05"
          at: "05:00"
          enabled: true
        - id: "morning-10"
          at: "10:00"
          enabled: true
        - id: "afternoon-15"
          at: "15:00"
          enabled: true
```

Each enabled schedule executes once daily in the configured IANA timezone. The plugin waits for the next daily time after startup and does not perform a startup warm-up. Duplicate enabled schedule times are rejected.

The plugin writes the latest run, per-credential results, and up to `max_log_entries` log entries to `state_file` using a temporary file and replacement (atomic on Unix). The file is created with owner-only permissions. The default path uses the operating system's user configuration directory. For Docker, set `state_file` to a path on a persistent volume, such as a writable path under the mounted plugin directory shown above. The Management key, credential tokens, request bodies, and raw error text are not stored there. History from releases before persistent state was added remains available only in the host logs.

Use the Management UI to add, edit, enable, or delete rows. The same operations are possible through CLIProxyAPI's standard endpoint:

```text
GET /v0/management/plugins/codex-5h-quota-warmer/config
PUT /v0/management/plugins/codex-5h-quota-warmer/config
```

## Plugin Management API

All routes below require the CPA Management key.

```text
GET  /v0/management/plugins/codex-5h-quota-warmer/status
POST /v0/management/plugins/codex-5h-quota-warmer/run
GET  /v0/management/plugins/codex-5h-quota-warmer/logs
```

`POST /run` starts one manual pass. A simultaneous automatic or manual pass returns HTTP 409. The bounded persistent log records configuration updates, each planned and triggered schedule, run starts, credential selection counts and skip reasons, each credential result, and run completion. The plugin sends schedule, selection, result, failure, and completion events to the host logger through `host.log` without credential secrets. Selection logs include disabled and missing-ID skip counts, plus the unavailable and inactive counts among credentials sent to CPA. A run with zero eligible credentials produces a warning and completion log with `credential_count: 0`. A storage failure appears in the status response and the host log.

## Build and Install

From this repository on Linux, build the shared library:

```bash
cd go
go build -buildmode=c-shared -o ../codex-5h-quota-warmer.so .
```

Install `codex-5h-quota-warmer.so` into the proxy server's `plugins/linux/$(go env GOARCH)/` directory, then enable the plugin using the configuration above and restart the proxy server. Use `.dylib` on macOS and `.dll` on Windows. The dynamic-library basename must remain `codex-5h-quota-warmer` so it matches `plugins.configs.codex-5h-quota-warmer`.

GitHub Releases provide platform ZIP files named `codex-5h-quota-warmer_<version>_<goos>_<goarch>.zip` and a `checksums.txt` file. Each ZIP contains the dynamic library at its root. Tagged releases are built for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

