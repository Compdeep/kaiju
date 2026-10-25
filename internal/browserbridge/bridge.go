package browserbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Compdeep/kaiju/internal/db"
)

// Store is where pairings are kept. *db.DB satisfies it; tests use a fake.
type Store interface {
	CreateBrowserPairCode(codeHash, username string, expires time.Time) error
	ClaimBrowserPairCode(codeHash string, dev db.BrowserDevice, secretHash string, now time.Time) (*db.BrowserDevice, error)
	BrowserDeviceBySecret(id, secretHash string) (*db.BrowserDevice, error)
	TouchBrowserDevice(id, name string, now time.Time) error
	ListBrowserDevices(username string) ([]db.BrowserDevice, error)
	RevokeBrowserDevice(id, username string) error
}

// Timing is every interval the bridge keeps. Fields so a test can run the
// heartbeat in milliseconds rather than waiting out the real one.
type Timing struct {
	Hello    time.Duration // how long a new socket has to say hello
	Ping     time.Duration // how often this side pings
	Silence  time.Duration // no frame at all for this long and the socket is dead
	Request  time.Duration // default deadline for one request
	Write    time.Duration // deadline for one frame to go out
	PairTTL  time.Duration // how long an unused pair code stays good
	Rejoin   time.Duration // how long an idempotent request waits for a reconnect
	ReadSize int64         // largest frame accepted; a screenshot is the big one
}

// DefaultTiming is what a running server uses.
//
// Ping at 15s with a 40s silence window: two pings may go unanswered before
// the socket is called dead, so one slow reply is not a disconnect. The
// extension pings on its own 20s clock as well, and either side's frames count
// as life — what is measured is silence, not missed pongs.
func DefaultTiming() Timing {
	return Timing{
		Hello:    10 * time.Second,
		Ping:     15 * time.Second,
		Silence:  40 * time.Second,
		Request:  20 * time.Second,
		Write:    10 * time.Second,
		PairTTL:  10 * time.Minute,
		Rejoin:   8 * time.Second,
		ReadSize: 32 << 20,
	}
}

// Errors a caller can tell apart.
var (
	ErrNoBrowser    = errors.New("no browser is connected — pair one with browser_pair, then check the Kaiju Browser Relay extension says Connected")
	ErrDisconnected = errors.New("the browser disconnected before it answered")
	ErrTimeout      = errors.New("the browser did not answer in time")
)

// RemoteError is the extension saying it could not do what was asked.
type RemoteError struct {
	Op      string
	Message string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("browser: %s: %s", e.Op, e.Message) }

/*
 * Bridge holds every connected browser and routes requests to them.
 * desc: One per server. Safe for concurrent use: the websocket handler, the
 *       tools and the status endpoint all reach it at once.
 */
type Bridge struct {
	store  Store
	timing Timing
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*session // device id → its live socket
	joined   chan struct{}       // closed and replaced whenever a session registers

	upgrader websocket.Upgrader
}

/*
 * New returns a bridge over the given store with the default timing.
 * param: store - where pairings live.
 * return: the bridge.
 */
func New(store Store) *Bridge {
	return NewWithTiming(store, DefaultTiming())
}

/*
 * NewWithTiming returns a bridge with the given intervals.
 * param: store - where pairings live.
 * param: t - the intervals; see Timing.
 * return: the bridge.
 */
func NewWithTiming(store Store, t Timing) *Bridge {
	b := &Bridge{
		store:    store,
		timing:   t,
		now:      time.Now,
		sessions: make(map[string]*session),
		joined:   make(chan struct{}),
	}
	b.upgrader = websocket.Upgrader{CheckOrigin: allowOrigin}
	return b
}

// allowOrigin admits the extension and nothing else that runs in a browser.
//
// The socket is reachable by any page the person has open, and a websocket
// handshake is not stopped by the rule that keeps one site from reading
// another's replies. A pairing credential is still needed to get anywhere, but
// a page has no business trying: so a page on the web is refused at the door,
// and only an extension origin — or a caller with no Origin, which is not a
// browser at all — is let through to say hello.
func allowOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "chrome-extension", "moz-extension", "extension":
		return true
	}
	return false
}

// ─── Pairing ────────────────────────────────────────────────────────────────

/*
 * Pair creates a pair code for one person.
 * desc: The code is shown once, works once, and expires after Timing.PairTTL
 *       if nobody uses it. What it pairs belongs to username.
 * param: username - who the browser will act for; empty on a node with no sign-in.
 * return: the code, when it expires, and any error storing it.
 */
func (b *Bridge) Pair(username string) (string, time.Time, error) {
	code := "kjp-" + randomToken(15)
	expires := b.now().Add(b.timing.PairTTL)
	if err := b.store.CreateBrowserPairCode(hashOf(code), username, expires); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

/*
 * Revoke removes a paired browser and drops it if it is connected.
 * param: username - whose device; only the owner may revoke it.
 * param: deviceID - the device.
 * return: db.ErrBrowserDeviceUnknown when there is no such device of theirs.
 */
func (b *Bridge) Revoke(username, deviceID string) error {
	if err := b.store.RevokeBrowserDevice(deviceID, username); err != nil {
		return err
	}
	b.mu.Lock()
	s := b.sessions[deviceID]
	b.mu.Unlock()
	if s != nil {
		s.shut(CloseRevoked, "revoked")
	}
	return nil
}

// randomToken is n random bytes, base32 without padding, lower case: long
// enough not to guess, and made only of letters and digits so a double-click
// selects all of it when it is copied out of a chat.
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("browserbridge: no randomness: " + err.Error())
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ─── Connections ────────────────────────────────────────────────────────────

/*
 * Handler is the websocket endpoint the extension dials.
 * desc: Not behind the sign-in: an extension has no session token. It proves
 *       who it is in its first frame instead, with a pair code or a device
 *       secret, and a socket that does not do so within Timing.Hello is closed.
 * return: the handler.
 */
func (b *Bridge) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := b.upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[browser] upgrade refused: %v", err)
			return
		}
		conn.SetReadLimit(b.timing.ReadSize)
		s, err := b.handshake(conn)
		if err != nil {
			log.Printf("[browser] handshake: %v", err)
			return
		}
		s.run()
	}
}

// handshake reads the hello, authenticates it, and registers the session.
// On any failure it has already told the extension why and closed the socket.
func (b *Bridge) handshake(conn *websocket.Conn) (*session, error) {
	conn.SetReadDeadline(b.now().Add(b.timing.Hello))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("no hello: %w", err)
	}
	var hello frame
	if err := json.Unmarshal(raw, &hello); err != nil || hello.T != "hello" {
		refuse(conn, CloseProtocol, "protocol", "the first frame must be a hello")
		return nil, errors.New("first frame was not a hello")
	}
	if hello.V != ProtocolVersion {
		refuse(conn, CloseProtocol, "version",
			fmt.Sprintf("this server speaks protocol %d and the extension sent %d — update whichever is older", ProtocolVersion, hello.V))
		return nil, fmt.Errorf("protocol version %d", hello.V)
	}

	now := b.now()
	name := strings.TrimSpace(hello.Name)
	if name == "" {
		name = "browser"
	}
	var dev *db.BrowserDevice
	secret := ""
	switch {
	case hello.PairCode != "":
		secret = randomToken(32)
		dev, err = b.store.ClaimBrowserPairCode(hashOf(strings.TrimSpace(hello.PairCode)),
			db.BrowserDevice{ID: "brw-" + randomToken(10), Name: name}, hashOf(secret), now)
	case hello.DeviceID != "" && hello.Secret != "":
		dev, err = b.store.BrowserDeviceBySecret(hello.DeviceID, hashOf(hello.Secret))
	default:
		err = errors.New("hello carried neither a pair code nor a device secret")
	}
	if err != nil {
		refuse(conn, CloseAuthFailed, "auth", "not paired: "+err.Error()+" — create a new pair code and paste it into the extension")
		return nil, fmt.Errorf("auth: %w", err)
	}
	if err := b.store.TouchBrowserDevice(dev.ID, name, now); err != nil {
		log.Printf("[browser] touch %s: %v", dev.ID, err)
	}
	dev.Name = name

	s := &session{
		b:           b,
		dev:         *dev,
		conn:        conn,
		version:     hello.Version,
		userAgent:   hello.UserAgent,
		connectedAt: now,
		lastSeen:    now,
		activeAt:    now,
		pending:     make(map[uint64]chan frame),
		done:        make(chan struct{}),
	}
	welcome := frame{T: "welcome", V: ProtocolVersion, DeviceID: dev.ID, Secret: secret,
		PingMS: int(b.timing.Ping / time.Millisecond), Server: "kaiju", Name: name}
	if err := s.send(welcome); err != nil {
		conn.Close()
		return nil, fmt.Errorf("welcome: %w", err)
	}
	b.register(s)
	log.Printf("[browser] connected: %s (%s) for %q, extension %s", dev.Name, dev.ID, dev.Username, hello.Version)
	return s, nil
}

// refuse tells the extension why, then closes with a code it can act on.
func refuse(conn *websocket.Conn, code int, kind, msg string) {
	deadline := time.Now().Add(2 * time.Second)
	if b, err := json.Marshal(frame{T: "error", Code: kind, Message: msg}); err == nil {
		conn.SetWriteDeadline(deadline)
		conn.WriteMessage(websocket.TextMessage, b)
	}
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, kind), deadline)
	conn.Close()
}

// register makes s the live session for its device, closing any older one.
// The same browser reconnecting before its old socket timed out is normal —
// a laptop waking up does it — and the newer socket is the one that works.
func (b *Bridge) register(s *session) {
	b.mu.Lock()
	old := b.sessions[s.dev.ID]
	b.sessions[s.dev.ID] = s
	close(b.joined)
	b.joined = make(chan struct{})
	b.mu.Unlock()
	if old != nil {
		old.shut(CloseReplaced, "replaced by a newer connection")
	}
}

// unregister removes s, unless a newer session already took its place.
func (b *Bridge) unregister(s *session) {
	b.mu.Lock()
	if b.sessions[s.dev.ID] == s {
		delete(b.sessions, s.dev.ID)
	}
	b.mu.Unlock()
}

// ─── Sessions ───────────────────────────────────────────────────────────────

type session struct {
	b           *Bridge
	dev         db.BrowserDevice
	conn        *websocket.Conn
	version     string
	userAgent   string
	connectedAt time.Time

	writeMu sync.Mutex // gorilla allows one writer at a time

	mu       sync.Mutex
	pending  map[uint64]chan frame
	nextID   uint64
	lastSeen time.Time // any frame at all
	activeAt time.Time // the person did something in this browser
	pingSeq  uint64
	pingAt   time.Time
	rtt      time.Duration
	tabCount int
	closeErr string

	done      chan struct{}
	closeOnce sync.Once
}

func (s *session) send(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(s.b.timing.Write))
	return s.conn.WriteMessage(websocket.TextMessage, b)
}

// shut closes the socket with a reason the extension can read. Safe to call
// more than once and from any goroutine; only the first call does anything.
func (s *session) shut(code int, reason string) {
	s.closeOnce.Do(func() {
		// The first reason is the true one: a read that failed has already said
		// why, and the "connection ended" that cleanup sends after it is not news.
		s.mu.Lock()
		if s.closeErr == "" {
			s.closeErr = reason
		}
		s.mu.Unlock()
		s.writeMu.Lock()
		s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		s.writeMu.Unlock()
		s.conn.Close()
	})
}

// run is the session's life: a ping loop beside a read loop, until either ends.
func (s *session) run() {
	stop := make(chan struct{})
	go s.pingLoop(stop)
	s.readLoop()
	close(stop)
	s.shut(CloseTimeout, "connection ended")
	s.b.unregister(s)

	// Everything still waiting gets an answer now rather than at its deadline.
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[uint64]chan frame)
	reason := s.closeErr
	s.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	close(s.done)
	log.Printf("[browser] disconnected: %s (%s): %s", s.dev.Name, s.dev.ID, reason)
}

func (s *session) readLoop() {
	for {
		// Silence, not missed pongs, is what kills a socket: any frame from the
		// extension is proof it is alive, so each one pushes the deadline out.
		s.conn.SetReadDeadline(time.Now().Add(s.b.timing.Silence))
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			s.mu.Lock()
			if s.closeErr == "" {
				s.closeErr = err.Error()
			}
			s.mu.Unlock()
			return
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			log.Printf("[browser] %s: unreadable frame dropped: %v", s.dev.ID, err)
			continue
		}
		now := time.Now()
		s.mu.Lock()
		s.lastSeen = now
		s.mu.Unlock()

		switch f.T {
		case "ping":
			if err := s.send(frame{T: "pong", Seq: f.Seq}); err != nil {
				return
			}
		case "pong":
			s.mu.Lock()
			if f.Seq == s.pingSeq && !s.pingAt.IsZero() {
				s.rtt = now.Sub(s.pingAt)
			}
			s.mu.Unlock()
		case "res":
			s.mu.Lock()
			ch := s.pending[f.ID]
			delete(s.pending, f.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		case "event":
			s.onEvent(f, now)
		}
	}
}

func (s *session) pingLoop(stop <-chan struct{}) {
	t := time.NewTicker(s.b.timing.Ping)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.pingSeq++
			seq := s.pingSeq
			s.pingAt = time.Now()
			s.mu.Unlock()
			if err := s.send(frame{T: "ping", Seq: seq}); err != nil {
				s.shut(CloseTimeout, "ping failed: "+err.Error())
				return
			}
		}
	}
}

// onEvent keeps what the status report needs from the extension's events.
// The tab list itself is always asked for fresh; a cache of it would be the
// thing a tool trusted after the person had closed half of it.
func (s *session) onEvent(f frame, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Ev {
	case "tab.activated", "tab.created", "window.focused":
		s.activeAt = now
	}
	var d struct {
		TabCount *int `json:"tab_count"`
	}
	if json.Unmarshal(f.Data, &d) == nil && d.TabCount != nil {
		s.tabCount = *d.TabCount
	}
}

// request sends one req and waits for its res.
func (s *session) request(ctx context.Context, op string, args json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	ch := make(chan frame, 1)
	s.mu.Lock()
	if s.closeErr != "" {
		s.mu.Unlock()
		return nil, ErrDisconnected
	}
	s.nextID++
	id := s.nextID
	s.pending[id] = ch
	s.mu.Unlock()

	forget := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	if err := s.send(frame{T: "req", ID: id, Op: op, Args: args}); err != nil {
		forget()
		s.shut(CloseTimeout, "write failed: "+err.Error())
		return nil, ErrDisconnected
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case f, ok := <-ch:
		if !ok {
			return nil, ErrDisconnected
		}
		if !f.OK {
			msg := f.Error
			if msg == "" {
				msg = "failed without saying why"
			}
			return nil, &RemoteError{Op: op, Message: msg}
		}
		return f.Result, nil
	case <-timer.C:
		forget()
		return nil, ErrTimeout
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// ─── Calls ──────────────────────────────────────────────────────────────────

/*
 * Call is one request for a browser to do something.
 * desc: Username is whose browser: a run may only drive a browser its own
 *       principal paired. Browser narrows to one device by id or name when the
 *       person has more than one; empty takes the one they used most recently.
 */
type Call struct {
	Username string
	Browser  string
	Op       string
	Args     any
	Timeout  time.Duration

	// Idempotent says the call may be sent again if the browser drops before
	// answering. True for reads. Never for a click or a keystroke: the first
	// one may have landed before the socket went, and doing it twice is worse
	// than reporting that it may not have happened.
	Idempotent bool
}

/*
 * Do sends a call to the person's browser and returns its result.
 * desc: An idempotent call that loses its browser mid-flight waits up to
 *       Timing.Rejoin for the same browser to reconnect and is sent once more.
 * param: ctx - cancels the wait.
 * param: c - the call.
 * return: the extension's result, or an error saying which side failed.
 */
func (b *Bridge) Do(ctx context.Context, c Call) (json.RawMessage, error) {
	args, err := json.Marshal(c.Args)
	if err != nil {
		return nil, fmt.Errorf("browser: %s: arguments: %w", c.Op, err)
	}
	if c.Args == nil {
		args = json.RawMessage(`{}`)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = b.timing.Request
	}

	s, err := b.pick(c.Username, c.Browser)
	if err != nil {
		return nil, err
	}
	res, err := s.request(ctx, c.Op, args, timeout)
	if !errors.Is(err, ErrDisconnected) || !c.Idempotent {
		return res, err
	}
	s, werr := b.waitFor(ctx, c.Username, s.dev.ID, b.timing.Rejoin)
	if werr != nil {
		return nil, err
	}
	return s.request(ctx, c.Op, args, timeout)
}

// pick chooses the session a call goes to.
func (b *Bridge) pick(username, browser string) (*session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var best *session
	var names []string
	for _, s := range b.sessions {
		if s.dev.Username != username {
			continue
		}
		names = append(names, s.dev.Name)
		if browser != "" && s.dev.ID != browser && !strings.EqualFold(s.dev.Name, browser) {
			continue
		}
		s.mu.Lock()
		active := s.activeAt
		s.mu.Unlock()
		if best == nil {
			best = s
			continue
		}
		best.mu.Lock()
		bestActive := best.activeAt
		best.mu.Unlock()
		if active.After(bestActive) {
			best = s
		}
	}
	if best != nil {
		return best, nil
	}
	if browser != "" && len(names) > 0 {
		sort.Strings(names)
		return nil, fmt.Errorf("no connected browser is called %q; connected: %s", browser, strings.Join(names, ", "))
	}
	return nil, ErrNoBrowser
}

// waitFor blocks until the given device has a live session, or the wait ends.
func (b *Bridge) waitFor(ctx context.Context, username, deviceID string, wait time.Duration) (*session, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		s := b.sessions[deviceID]
		joined := b.joined
		b.mu.Unlock()
		if s != nil && s.dev.Username == username {
			s.mu.Lock()
			alive := s.closeErr == ""
			s.mu.Unlock()
			if alive {
				return s, nil
			}
		}
		select {
		case <-joined:
		case <-deadline.C:
			return nil, ErrDisconnected
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ─── Status ─────────────────────────────────────────────────────────────────

// BrowserStatus is one paired browser, connected or not.
type BrowserStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	Extension   string `json:"extension,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
	PingMS      int64  `json:"ping_ms,omitempty"`
	Tabs        int    `json:"tabs,omitempty"`
	Pending     int    `json:"pending,omitempty"`
}

/*
 * Status reports every browser one person has paired.
 * desc: Connected ones first, with their round-trip time and last frame; then
 *       the ones paired but not connected right now, so "not connected" can be
 *       told apart from "never paired".
 * param: username - whose browsers.
 * return: the list, and an error only if the store could not be read.
 */
func (b *Bridge) Status(username string) ([]BrowserStatus, error) {
	var out []BrowserStatus
	live := map[string]bool{}
	b.mu.Lock()
	for _, s := range b.sessions {
		if s.dev.Username != username {
			continue
		}
		s.mu.Lock()
		st := BrowserStatus{
			ID: s.dev.ID, Name: s.dev.Name, Connected: s.closeErr == "",
			Extension: s.version, UserAgent: s.userAgent,
			ConnectedAt: s.connectedAt.UTC().Format(time.RFC3339),
			LastSeen:    s.lastSeen.UTC().Format(time.RFC3339),
			PingMS:      s.rtt.Milliseconds(), Tabs: s.tabCount, Pending: len(s.pending),
		}
		s.mu.Unlock()
		live[s.dev.ID] = true
		out = append(out, st)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	devices, err := b.store.ListBrowserDevices(username)
	if err != nil {
		return out, err
	}
	for _, d := range devices {
		if live[d.ID] {
			continue
		}
		st := BrowserStatus{ID: d.ID, Name: d.Name}
		if d.LastSeen > 0 {
			st.LastSeen = time.Unix(d.LastSeen, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, st)
	}
	return out, nil
}
