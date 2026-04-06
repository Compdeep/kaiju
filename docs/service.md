# Service Tool

Kaiju's `service` tool manages long-running processes — servers, daemons, dev servers, watchers. It exists so the planner can start a server without blocking the investigation.

## The problem it solves

Before `service`, the planner emitted `bash("node server.js")` to start a server. `bash` waits for the command to exit. Servers don't exit. The bash node hung until the timeout fired, wall-clock expired, and the investigation aborted — even though the server had started successfully in the background.

`service(action="start", ...)` spawns the process in a detached session and returns in ~100ms. The investigation continues immediately.

## Interface

```
service({
  "action":  "start" | "stop" | "restart" | "status" | "logs" | "list" | "remove",
  "name":    "kaiju-backend",
  "command": "node server.js",       // required for start
  "workdir": "/path/to/project",     // optional, defaults to workspace
  "lines":   50,                      // optional, for logs
  "stream":  "out" | "err" | "both"   // optional, for logs
})
```

Seven actions, one tool, one schema.

## Actions

| Action | Effect |
|--------|--------|
| `start` | Spawns the command in a detached session. Records PID + log file paths in the registry. Returns immediately. |
| `stop` | Sends SIGTERM, waits up to 5s, then SIGKILL if still alive. |
| `restart` | Stop + start using the registry's command and workdir. |
| `status` | Returns `{pid, alive, uptime_sec, command, log paths}` for a named service. |
| `logs` | Tails the stdout/stderr log file by line count. Structured output for `param_refs` chaining. |
| `list` | All services in the registry with live status. |
| `remove` | Deletes a stopped service from the registry (refuses if still running). |

## How it spawns

Native backend uses Go's `exec.Command` with `SysProcAttr{Setsid: true}`:

```go
cmd := exec.Command("sh", "-c", command)
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
cmd.Stdout = openAppend(logOut)
cmd.Stderr = openAppend(logErr)
cmd.Start()              // non-blocking
cmd.Process.Release()    // detached, kaiju doesn't reap it
```

`Setsid` makes the child its own session leader, so it survives kaiju exit. `Release` drops the reaper reference so kaiju doesn't block on it. No nohup, no shell tricks.

## Registry

Services kaiju started itself are tracked in `<workspace>/.services.json`:

```json
[
  {
    "name": "kaiju-backend",
    "command": "node server.js",
    "workdir": "/path/to/backend",
    "pid": 12345,
    "started_at": "2026-04-05T07:30:00Z",
    "status": "running",
    "log_out": "/path/to/workspace/.services/kaiju-backend.out.log",
    "log_err": "/path/to/workspace/.services/kaiju-backend.err.log"
  }
]
```

Registry only holds services kaiju spawned. systemd, pm2, launchd services are NOT tracked here — they have their own state and would be duplicated by tracking.

## Log files

- stdout: `<workspace>/.services/<name>.out.log`
- stderr: `<workspace>/.services/<name>.err.log`

Both are append-mode, no rotation in v1. `service(action="logs", name="...")` tails them by line count.

## Idempotency

`start` checks the registry first. If a service with the same name is already running (PID alive via `kill -0`), it returns without spawning a duplicate. Lets the planner retry safely without starting multiple copies.

## Planner integration

The native planner prompt has a hard rule:

> ALWAYS use the service tool for long-running processes (servers, daemons, dev servers, watchers, listeners). NEVER use bash for foreground servers — bash blocks the investigation waiting for the command to exit, which servers never do. service(action="start", name="...", command="...") spawns in the background and returns immediately.

The planner picks `service` whenever the task involves starting something that doesn't terminate.

## What's NOT in v1

- **Systemd / pm2 / launchd proxies** — the tool only has the native backend. Adding proxies later would let it control existing OS services (stash: see `plan_service_tool.md` memory).
- **Auto-restart on crash** — no health check goroutine in v1. Services that crash stay crashed until explicitly restarted.
- **Log rotation** — logs grow unbounded. Truncate manually if they get big.
- **Startup reconciliation** — on kaiju restart, the registry is not re-scanned. Services from a previous kaiju run appear as "running" in the registry but may actually be dead. Run `service(action="status", ...)` to reconcile the state.

These are deliberate omissions. Each is easy to add if a user hits the limitation. Starting lean.

## Configuration

Service tool is registered unconditionally in `cmd/kaiju/main.go`:

```go
reg.Replace(kaijutools.NewService(cfg.Agent.Workspace), "builtin")
```

No config flag. Always available.

## Impact level

`service` is declared as `ImpactAffect` (operate) by default. Actions like `start`, `stop`, `restart`, `remove` all count as operate-level. Only `status`, `logs`, and `list` are observe-level. The gate enforces this like any other tool.

## Implementation

`internal/tools/service.go` — single file, ~440 lines. Reuses existing tool infrastructure. No new packages, no new dependencies.
