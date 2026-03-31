// Package ipc provides a compatibility shim for the omamori C++ IPC bridge.
// The agent only references ipc.Envelope in its IPCSender interface definition.
// No actual IPC communication happens in kaiju.
package ipc

import "encoding/json"

// Envelope is an IPC message envelope (C++ bridge format).
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
