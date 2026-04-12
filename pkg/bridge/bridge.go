// Package bridge provides a lightweight IPC protocol for integrating kaiju
// with external processes via newline-delimited JSON (NDJSON) over stdio,
// named pipes, or unix sockets.
//
// Any program that can read/write NDJSON can communicate with kaiju as a
// bridge — enabling IDE integration, plugin systems, monitoring dashboards,
// and CI/CD pipelines.
//
// The protocol supports three communication patterns:
//   - Fire-and-forget (async): send a message, no response expected
//   - Request/response (sync): send with req_id, block until matching response
//   - Broadcast (push): kaiju pushes events to the external process
package bridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Envelope is the wire format for all bridge messages.
type Envelope struct {
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data,omitempty"`
	ReqID string          `json:"req_id,omitempty"`
}

// Config holds bridge configuration.
type Config struct {
	Enabled           bool   `json:"enabled"`
	Transport         string `json:"transport"`           // "stdio", "pipe", "unix"
	PipePath          string `json:"pipe_path,omitempty"` // named pipe or unix socket path
	StatusIntervalSec int    `json:"status_interval_sec"` // heartbeat interval (default: 10)
}

// DefaultConfig returns bridge configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:           false,
		Transport:         "stdio",
		StatusIntervalSec: 10,
	}
}

// Bridge handles bidirectional NDJSON communication with an external process.
type Bridge struct {
	reader  *bufio.Scanner
	writer  io.Writer
	writeMu sync.Mutex

	incoming chan Envelope

	pendingMu sync.Mutex
	pending   map[string]chan Envelope
}

// NewBridge creates a bridge over any reader/writer pair.
func NewBridge(r io.Reader, w io.Writer) *Bridge {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // 256KB line buffer
	return &Bridge{
		reader:   scanner,
		writer:   w,
		incoming: make(chan Envelope, 64),
		pending:  make(map[string]chan Envelope),
	}
}

// Send writes an envelope to the external process. Thread-safe.
func (b *Bridge) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("bridge: marshal: %w", err)
	}
	data = append(data, '\n')

	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err = b.writer.Write(data)
	return err
}

// SendRequest sends an envelope with an auto-generated req_id and blocks
// until a response with a matching req_id arrives. Thread-safe.
func (b *Bridge) SendRequest(ctx context.Context, env Envelope) (Envelope, error) {
	// Generate random correlation ID
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	env.ReqID = hex.EncodeToString(idBytes)

	// Register pending response channel
	ch := make(chan Envelope, 1)
	b.pendingMu.Lock()
	b.pending[env.ReqID] = ch
	b.pendingMu.Unlock()

	defer func() {
		b.pendingMu.Lock()
		delete(b.pending, env.ReqID)
		b.pendingMu.Unlock()
	}()

	// Send the request
	if err := b.Send(env); err != nil {
		return Envelope{}, err
	}

	// Wait for response or context cancellation
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
}

// ReadLoop continuously reads NDJSON from the reader and routes messages.
// Messages with a matching req_id go to pending request channels.
// All others go to the Incoming() channel.
// Blocks until ctx is cancelled or the reader returns EOF.
func (b *Bridge) ReadLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			close(b.incoming)
			return
		default:
		}

		if !b.reader.Scan() {
			close(b.incoming)
			return
		}

		var env Envelope
		if err := json.Unmarshal(b.reader.Bytes(), &env); err != nil {
			continue // skip malformed lines
		}

		// Route to pending request if req_id matches
		if env.ReqID != "" {
			b.pendingMu.Lock()
			if ch, ok := b.pending[env.ReqID]; ok {
				ch <- env
				b.pendingMu.Unlock()
				continue
			}
			b.pendingMu.Unlock()
		}

		// Otherwise send to general incoming channel
		select {
		case b.incoming <- env:
		default:
			// Drop if channel is full
		}
	}
}

// Incoming returns the channel for inbound messages that aren't
// responses to pending requests.
func (b *Bridge) Incoming() <-chan Envelope {
	return b.incoming
}
