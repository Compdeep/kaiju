# Bridge Module — External Process IPC

> **Status:** Optional module. Not required for standalone operation.
> **Package:** `pkg/bridge`

## Overview

The bridge module provides a lightweight IPC protocol for integrating kaiju with external processes. Any program that can read/write newline-delimited JSON (NDJSON) over stdin/stdout or a named pipe can communicate with kaiju as a bridge.

Originally built for omamori's C++ security engine bridge, the pattern is extracted here as a generic, language-agnostic integration layer.

## Architecture

```
┌──────────────┐   NDJSON/stdio    ┌──────────────┐
│    Kaiju     │◄─────────────────►│  External    │
│   (Go)       │   or named pipe   │  Process     │
└──────────────┘                   └──────────────┘
```

## Transport Options

| Transport | Platform | Use case |
|-----------|----------|----------|
| stdin/stdout | All | Subprocess mode — kaiju spawns or is spawned by external process |
| Named pipe | Windows | Service mode — `\\.\pipe\kaiju-ipc` with auto-reconnect |
| Unix socket | Linux/macOS | Daemon mode — `/tmp/kaiju.sock` |

## Protocol

### Framing

Messages are **newline-delimited JSON** (NDJSON). One JSON object per line, terminated by `\n`. Max line size: 256KB.

### Envelope Format

```json
{
  "type": "request_type",
  "data": { ... },
  "req_id": "a1b2c3d4e5f6"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | Yes | Message type discriminator |
| `data` | No | Opaque JSON payload |
| `req_id` | No | Correlation ID for request/response pairs |

### Communication Patterns

#### Fire-and-Forget (Async)

External process sends a message, no response expected:

```
External → Kaiju:  {"type": "notify", "data": {"event": "file_changed", "path": "/tmp/foo"}}
```

#### Request/Response (Sync)

External process sends a request with `req_id`, kaiju responds with matching `req_id`:

```
External → Kaiju:  {"type": "query", "data": {"question": "summarize this file"}, "req_id": "abc123"}
Kaiju → External:  {"type": "query_response", "data": {"answer": "..."}, "req_id": "abc123"}
```

Correlation is done by the `req_id` field (16-char random hex). The sender blocks until a response with matching `req_id` arrives. Context-based timeout prevents hangs.

#### Broadcast (Kaiju → External)

Kaiju pushes events to the external process:

```
Kaiju → External:  {"type": "status", "data": {"investigating": true, "nodes": 5}}
Kaiju → External:  {"type": "dag_event", "data": {"event": "node_complete", "skill": "bash", "result": "..."}}
```

## Message Types

### Inbound (External → Kaiju)

| Type | Purpose | Response |
|------|---------|----------|
| `query` | Submit a question/task to the agent | `query_response` |
| `trigger` | Queue a trigger for investigation | None (async) |
| `tool_call` | Execute a specific tool directly | `tool_response` |
| `interject` | Inject a human message during active investigation | None |
| `config` | Runtime config update | `config_ack` |
| `ping` | Health check | `pong` |

### Outbound (Kaiju → External)

| Type | Purpose |
|------|---------|
| `query_response` | Answer to a `query` request |
| `tool_response` | Result of a `tool_call` request |
| `config_ack` | Confirmation of config update |
| `pong` | Response to `ping` |
| `status` | Periodic status heartbeat (every 10s) |
| `dag_event` | Real-time DAG execution events |
| `verdict` | Final investigation verdict |

## Bridge API

```go
// Bridge handles bidirectional NDJSON communication.
type Bridge struct { ... }

// NewBridge creates a bridge over any io.Reader + io.Writer.
func NewBridge(r io.Reader, w io.Writer) *Bridge

// Send writes an envelope (fire-and-forget). Thread-safe.
func (b *Bridge) Send(env Envelope) error

// SendRequest sends an envelope and blocks until a matching response arrives.
func (b *Bridge) SendRequest(ctx context.Context, env Envelope) (Envelope, error)

// ReadLoop continuously reads from the reader, routing messages.
// Messages with a matching req_id go to pending request channels.
// All others go to the Incoming() channel.
func (b *Bridge) ReadLoop(ctx context.Context)

// Incoming returns the channel for fire-and-forget inbound messages.
func (b *Bridge) Incoming() <-chan Envelope
```

## Thread Safety

- `Send()` is mutex-protected — safe for concurrent goroutines
- `ReadLoop()` runs in a single goroutine, routes to channels
- `SendRequest()` uses per-request buffered channels for correlation
- `Incoming()` channel is buffered (capacity 64)

## Use Cases

### IDE Integration

A VSCode/Neovim extension spawns kaiju as a subprocess, communicates via stdin/stdout:

```
Editor → Kaiju:  {"type": "query", "data": {"question": "explain this function", "context": "..."}, "req_id": "..."}
Kaiju → Editor:  {"type": "query_response", "data": {"answer": "This function..."}, "req_id": "..."}
```

### Plugin System

External tools register as bridges. Kaiju spawns them and routes tool calls:

```
Kaiju → Plugin:  {"type": "tool_call", "data": {"name": "docker_ps", "params": {}}, "req_id": "..."}
Plugin → Kaiju:  {"type": "tool_response", "data": {"result": "CONTAINER ID..."}, "req_id": "..."}
```

### Monitoring Dashboard

A web dashboard connects via unix socket, receives real-time events:

```
Kaiju → Dashboard:  {"type": "dag_event", "data": {"event": "node_start", "skill": "bash", "params": {"command": "ls"}}}
Kaiju → Dashboard:  {"type": "dag_event", "data": {"event": "node_complete", "skill": "bash", "result": "file1.txt\nfile2.txt"}}
Kaiju → Dashboard:  {"type": "verdict", "data": {"text": "The directory contains 2 files..."}}
```

### CI/CD Integration

A CI runner invokes kaiju for automated code review:

```
CI → Kaiju:  {"type": "query", "data": {"question": "review this diff for security issues", "context": "..."}, "req_id": "..."}
Kaiju → CI:  {"type": "query_response", "data": {"answer": "Found 2 issues: ..."}, "req_id": "..."}
```

## Configuration

Bridge is opt-in:

```json
{
  "bridge": {
    "enabled": true,
    "transport": "stdio",
    "status_interval_sec": 10
  }
}
```

Transport options: `"stdio"`, `"pipe"` (Windows), `"unix"` (Linux/macOS).
