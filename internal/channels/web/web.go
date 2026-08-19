package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/internal/channels"
	"github.com/gorilla/websocket"
)

// Which pages may open a socket here.
//
// A websocket is not covered by the rule that stops one site reading another's
// replies: the handshake is an ordinary request, and once it is accepted the
// page on the other end can send and receive freely. So accepting every origin
// meant any page in the same browser could open one of these and talk to the
// agent. Accepting only this host is the whole fix.
//
// A caller that sends no Origin at all is not a browser — a command-line client
// or another program — and is allowed, because the transport is what decides
// who reaches it.
var upgrader = websocket.Upgrader{
	CheckOrigin: sameHost,
}

/*
 * sameHost reports whether a handshake came from a page on this host.
 * desc: Compares the host in Origin with the host the request was addressed to.
 *       Hosts, not schemes: this is served over plain HTTP on a loopback
 *       address, so there is no scheme to compare. An Origin that will not
 *       parse is refused.
 * param: r - the handshake request.
 * return: true when the socket may be opened.
 */
func sameHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// wsMessage is the JSON wire format for WebSocket messages.
type wsMessage struct {
	Text      string `json:"text"`
	SessionID string `json:"session_id,omitempty"`
}

// Channel implements a WebSocket-based channel.
type Channel struct {
	mu    sync.RWMutex
	conns map[string]*websocket.Conn // sessionID → conn
	inbox chan<- channels.InboundMessage
}

// New creates a WebSocket channel. Call Handler() to get the HTTP handler.
func New() *Channel {
	return &Channel{
		conns: make(map[string]*websocket.Conn),
	}
}

func (c *Channel) ID() string { return "web" }

// Start stores the inbox reference and blocks until ctx is done.
// The actual message receiving happens in the HTTP handler goroutines.
func (c *Channel) Start(ctx context.Context, inbox chan<- channels.InboundMessage) error {
	c.inbox = inbox
	<-ctx.Done()
	return ctx.Err()
}

// Send delivers a message to the WebSocket client for the given session.
func (c *Channel) Send(_ context.Context, msg channels.OutboundMessage) error {
	c.mu.RLock()
	conn, ok := c.conns[msg.SessionID]
	c.mu.RUnlock()
	if !ok {
		log.Printf("[web] no connection for session %s", msg.SessionID)
		return nil
	}
	payload, _ := json.Marshal(wsMessage{Text: msg.Text, SessionID: msg.SessionID})
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Channel) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, conn := range c.conns {
		conn.Close()
		delete(c.conns, id)
	}
	return nil
}

// Handler returns an http.HandlerFunc that upgrades to WebSocket.
func (c *Channel) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[web] websocket upgrade: %v", err)
			return
		}

		sessionID := r.URL.Query().Get("session")
		if sessionID == "" {
			sessionID = "ws-" + time.Now().Format("20060102-150405.000")
		}

		c.mu.Lock()
		c.conns[sessionID] = conn
		c.mu.Unlock()

		log.Printf("[web] client connected: session=%s", sessionID)

		defer func() {
			c.mu.Lock()
			delete(c.conns, sessionID)
			c.mu.Unlock()
			conn.Close()
		}()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[web] read: %v", err)
				return
			}
			var msg wsMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("[web] unmarshal: %v", err)
				continue
			}
			if c.inbox != nil && msg.Text != "" {
				c.inbox <- channels.InboundMessage{
					ChannelID:  "web",
					SessionID:  sessionID,
					SenderID:   sessionID,
					SenderName: "web-user",
					Text:       msg.Text,
					Timestamp:  time.Now(),
				}
			}
		}
	}
}
