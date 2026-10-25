// Package browserbridge connects kaiju to a person's own browser through the
// Kaiju Browser Relay extension (extension/ at the root of this repository).
//
// The extension dials in; kaiju never dials out. Everything after the
// handshake is one JSON frame per websocket message, in both directions:
//
//	extension → kaiju   hello, ping, pong, res, event
//	kaiju → extension   welcome, ping, pong, req, error
//
// A request names an operation ("tabs.list") the extension carries out itself
// and answers with a res frame carrying the same id. Raw DevTools access is not
// the protocol; it is at most one operation among many. So a tool here asks for
// what it wants in one round trip, and the extension is where the browser's own
// rules — which tabs exist, whether an element is still there — are applied.
package browserbridge

import "encoding/json"

// ProtocolVersion is the frame format this side speaks. A hello naming any
// other version is refused, so an extension and a server that disagree say so
// at once instead of misreading each other's frames.
const ProtocolVersion = 1

// Close codes. In the 4000 range, which the websocket spec leaves to
// applications. The extension reads them to decide whether reconnecting can
// help: after an auth failure or a revocation it cannot, and retrying forever
// would only hammer the server with a credential that will never work.
const (
	CloseAuthFailed = 4001 // bad pair code or device secret: re-pair, do not retry
	CloseProtocol   = 4002 // a frame this side cannot read
	CloseTimeout    = 4008 // no frame within the heartbeat window
	CloseReplaced   = 4009 // the same device connected again; this socket is stale
	CloseRevoked    = 4010 // the device was revoked: re-pair, do not retry
)

// frame is every message on the wire. One struct rather than one per type,
// because a reader has to decode the type before it knows which it holds, and
// the fields that do not apply to a type are simply absent.
type frame struct {
	T string `json:"t"`

	// req / res
	ID     uint64          `json:"id,omitempty"`
	Op     string          `json:"op,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`

	// ping / pong
	Seq uint64 `json:"seq,omitempty"`

	// event
	Ev   string          `json:"ev,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	// hello: a pair code the first time, a device id and secret after that.
	V         int    `json:"v,omitempty"`
	PairCode  string `json:"pair_code,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`

	// welcome
	PingMS int    `json:"ping_ms,omitempty"`
	Server string `json:"server,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
