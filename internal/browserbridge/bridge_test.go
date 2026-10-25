package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Compdeep/kaiju/internal/db"
)

// fastTiming runs the heartbeat in milliseconds so a dead socket is noticed
// inside a test rather than after forty seconds.
func fastTiming() Timing {
	t := DefaultTiming()
	t.Hello = time.Second
	t.Ping = 40 * time.Millisecond
	t.Silence = 200 * time.Millisecond
	t.Request = time.Second
	t.Rejoin = time.Second
	return t
}

func newBridge(t *testing.T) (*Bridge, string) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "kaiju.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b := NewWithTiming(store, fastTiming())
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	return b, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// fakeExtension is the extension's side of the protocol, written plainly: it
// says hello, answers pings unless told to go quiet, and answers requests with
// whatever handle returns.
type fakeExtension struct {
	t      *testing.T
	conn   *websocket.Conn
	mu     sync.Mutex
	quiet  bool
	handle func(op string, args json.RawMessage) (any, string)
	closed chan int // the close code the server sent
}

func dial(t *testing.T, url string, hello frame, origin string) (*fakeExtension, frame) {
	t.Helper()
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	hello.T, hello.V = "hello", ProtocolVersion
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var reply frame
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("no reply to hello: %v", err)
	}
	x := &fakeExtension{t: t, conn: conn, closed: make(chan int, 1)}
	if reply.T == "welcome" {
		go x.loop()
	} else {
		conn.Close()
	}
	return x, reply
}

func (x *fakeExtension) loop() {
	for {
		var f frame
		if err := x.conn.ReadJSON(&f); err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				x.closed <- ce.Code
			} else {
				x.closed <- 0
			}
			return
		}
		x.mu.Lock()
		quiet, handle := x.quiet, x.handle
		x.mu.Unlock()
		if quiet {
			continue
		}
		switch f.T {
		case "ping":
			x.write(frame{T: "pong", Seq: f.Seq})
		case "req":
			go func(f frame) {
				var result any
				errMsg := "no handler"
				if handle != nil {
					result, errMsg = handle(f.Op, f.Args)
				}
				res := frame{T: "res", ID: f.ID, OK: errMsg == ""}
				if errMsg != "" {
					res.Error = errMsg
				} else {
					res.Result, _ = json.Marshal(result)
				}
				x.write(res)
			}(f)
		}
	}
}

func (x *fakeExtension) write(f frame) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.conn.WriteJSON(f)
}

func (x *fakeExtension) setQuiet(q bool) {
	x.mu.Lock()
	x.quiet = q
	x.mu.Unlock()
}

func (x *fakeExtension) setHandler(h func(string, json.RawMessage) (any, string)) {
	x.mu.Lock()
	x.handle = h
	x.mu.Unlock()
}

func (x *fakeExtension) waitClosed(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case code := <-x.closed:
		return code
	case <-time.After(within):
		t.Fatal("the server did not close the socket")
		return 0
	}
}

func tabsHandler(op string, _ json.RawMessage) (any, string) {
	if op != "tabs.list" {
		return nil, "unknown op " + op
	}
	return map[string]any{"browser": "home", "tabs": []map[string]any{{"id": 7, "title": "Example", "url": "https://example.com"}}}, ""
}

// pairAndConnect pairs a browser for username and returns it connected, with
// the device id and secret it was given.
func pairAndConnect(t *testing.T, b *Bridge, url, username, name string) (*fakeExtension, string, string) {
	t.Helper()
	code, _, err := b.Pair(username)
	if err != nil {
		t.Fatal(err)
	}
	x, welcome := dial(t, url, frame{PairCode: code, Name: name}, "chrome-extension://abcdef")
	if welcome.T != "welcome" || welcome.DeviceID == "" || welcome.Secret == "" {
		t.Fatalf("pairing did not return a device and secret: %+v", welcome)
	}
	return x, welcome.DeviceID, welcome.Secret
}

func TestPairCodeTradesForASecretAndWorksOnce(t *testing.T) {
	b, url := newBridge(t)
	code, _, err := b.Pair("alice")
	if err != nil {
		t.Fatal(err)
	}
	x, welcome := dial(t, url, frame{PairCode: code, Name: "home"}, "chrome-extension://abcdef")
	if welcome.T != "welcome" || welcome.Secret == "" {
		t.Fatalf("expected a welcome with a secret, got %+v", welcome)
	}
	defer x.conn.Close()

	// The same code again — as it would be if it were copied out of the
	// transcript afterwards — pairs nothing.
	_, reply := dial(t, url, frame{PairCode: code, Name: "thief"}, "chrome-extension://abcdef")
	if reply.T != "error" || reply.Code != "auth" {
		t.Fatalf("a used code was accepted: %+v", reply)
	}
}

func TestDeviceSecretReconnectsAndAnswersRequests(t *testing.T) {
	b, url := newBridge(t)
	first, id, secret := pairAndConnect(t, b, url, "alice", "home")
	first.conn.Close()

	x, welcome := dial(t, url, frame{DeviceID: id, Secret: secret, Name: "home"}, "chrome-extension://abcdef")
	if welcome.T != "welcome" || welcome.Secret != "" {
		t.Fatalf("reconnect should welcome without issuing a new secret: %+v", welcome)
	}
	defer x.conn.Close()
	x.setHandler(tabsHandler)

	res, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list"})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(res), "example.com") {
		t.Fatalf("unexpected result %s", res)
	}

	_, reply := dial(t, url, frame{DeviceID: id, Secret: "wrong", Name: "home"}, "chrome-extension://abcdef")
	if reply.T != "error" {
		t.Fatalf("a wrong secret was accepted: %+v", reply)
	}
}

func TestAWebPageCannotOpenTheSocket(t *testing.T) {
	_, url := newBridge(t)
	h := http.Header{}
	h.Set("Origin", "https://evil.example")
	_, resp, err := websocket.DefaultDialer.Dial(url, h)
	if err == nil {
		t.Fatal("a web page's handshake was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", resp)
	}
}

func TestSilenceClosesTheSocketAndFailsWhatIsWaiting(t *testing.T) {
	b, url := newBridge(t)
	x, _, _ := pairAndConnect(t, b, url, "alice", "home")
	defer x.conn.Close()

	// A request the browser never answers, on a socket that then goes quiet:
	// it must fail when the socket is declared dead, not at its own deadline.
	x.setQuiet(true)
	start := time.Now()
	_, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list", Timeout: 5 * time.Second})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("expected ErrDisconnected, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the waiting request took %v to fail; it should fail when the socket dies", time.Since(start))
	}
	x.waitClosed(t, time.Second)

	if _, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list"}); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("after the socket died, expected ErrNoBrowser, got %v", err)
	}
}

func TestPingsKeepAQuietButLiveSocketOpen(t *testing.T) {
	b, url := newBridge(t)
	x, _, _ := pairAndConnect(t, b, url, "alice", "home")
	defer x.conn.Close()
	x.setHandler(tabsHandler)

	// Several silence windows with no requests: the pongs alone keep it alive.
	time.Sleep(3 * fastTiming().Silence)
	if _, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list"}); err != nil {
		t.Fatalf("a socket answering pings was dropped: %v", err)
	}
	list, err := b.Status("alice")
	if err != nil || len(list) != 1 || !list[0].Connected {
		t.Fatalf("status should show one connected browser: %+v %v", list, err)
	}
}

func TestANewerConnectionReplacesTheOld(t *testing.T) {
	b, url := newBridge(t)
	old, id, secret := pairAndConnect(t, b, url, "alice", "home")
	defer old.conn.Close()

	fresh, _ := dial(t, url, frame{DeviceID: id, Secret: secret, Name: "home"}, "chrome-extension://abcdef")
	defer fresh.conn.Close()
	fresh.setHandler(tabsHandler)

	if code := old.waitClosed(t, time.Second); code != CloseReplaced {
		t.Fatalf("the old socket was closed with %d, want %d", code, CloseReplaced)
	}
	if _, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list"}); err != nil {
		t.Fatalf("the request should go to the newer socket: %v", err)
	}
}

func TestAnIdempotentCallIsSentAgainAfterAReconnect(t *testing.T) {
	b, url := newBridge(t)
	x, id, secret := pairAndConnect(t, b, url, "alice", "home")

	// The first socket drops the moment it is asked; the browser comes back a
	// moment later, as the extension's reconnect loop would bring it.
	x.setHandler(func(string, json.RawMessage) (any, string) {
		x.conn.Close()
		go func() {
			time.Sleep(50 * time.Millisecond)
			y, _ := dial(t, url, frame{DeviceID: id, Secret: secret, Name: "home"}, "chrome-extension://abcdef")
			y.setHandler(tabsHandler)
			t.Cleanup(func() { y.conn.Close() })
		}()
		return nil, "dropping"
	})

	res, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list", Idempotent: true})
	if err != nil {
		t.Fatalf("the read should have been retried on the reconnected browser: %v", err)
	}
	if !strings.Contains(string(res), "example.com") {
		t.Fatalf("unexpected result %s", res)
	}
}

func TestAClickIsNotSentTwice(t *testing.T) {
	b, url := newBridge(t)
	x, _, _ := pairAndConnect(t, b, url, "alice", "home")
	x.setHandler(func(string, json.RawMessage) (any, string) {
		x.conn.Close()
		return nil, "dropping"
	})
	_, err := b.Do(context.Background(), Call{Username: "alice", Op: "input.click"})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("a non-idempotent call should report the disconnect, got %v", err)
	}
}

func TestOnePersonCannotReachAnothersBrowser(t *testing.T) {
	b, url := newBridge(t)
	x, _, _ := pairAndConnect(t, b, url, "alice", "home")
	defer x.conn.Close()
	x.setHandler(tabsHandler)

	if _, err := b.Do(context.Background(), Call{Username: "bob", Op: "tabs.list"}); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("bob reached alice's browser: %v", err)
	}
	if list, _ := b.Status("bob"); len(list) != 0 {
		t.Fatalf("bob sees alice's browser in status: %+v", list)
	}
}

func TestRevokingDropsTheBrowserAndItsSecret(t *testing.T) {
	b, url := newBridge(t)
	x, id, secret := pairAndConnect(t, b, url, "alice", "home")
	defer x.conn.Close()

	if err := b.Revoke("bob", id); err == nil {
		t.Fatal("bob revoked alice's browser")
	}
	if err := b.Revoke("alice", id); err != nil {
		t.Fatal(err)
	}
	if code := x.waitClosed(t, time.Second); code != CloseRevoked {
		t.Fatalf("closed with %d, want %d", code, CloseRevoked)
	}
	_, reply := dial(t, url, frame{DeviceID: id, Secret: secret, Name: "home"}, "chrome-extension://abcdef")
	if reply.T != "error" {
		t.Fatalf("a revoked device reconnected: %+v", reply)
	}
}

func TestTheBrowserMostRecentlyUsedIsChosen(t *testing.T) {
	b, url := newBridge(t)
	home, _, _ := pairAndConnect(t, b, url, "alice", "home")
	defer home.conn.Close()
	work, _, _ := pairAndConnect(t, b, url, "alice", "work")
	defer work.conn.Close()
	home.setHandler(func(string, json.RawMessage) (any, string) { return map[string]string{"browser": "home"}, "" })
	work.setHandler(func(string, json.RawMessage) (any, string) { return map[string]string{"browser": "work"}, "" })

	// The person switches to a tab in the home browser.
	home.write(frame{T: "event", Ev: "tab.activated", Data: json.RawMessage(`{"tab_count":3}`)})
	time.Sleep(50 * time.Millisecond)

	res, err := b.Do(context.Background(), Call{Username: "alice", Op: "tabs.list"})
	if err != nil || !strings.Contains(string(res), "home") {
		t.Fatalf("expected the home browser, got %s %v", res, err)
	}
	res, err = b.Do(context.Background(), Call{Username: "alice", Browser: "work", Op: "tabs.list"})
	if err != nil || !strings.Contains(string(res), "work") {
		t.Fatalf("naming the browser should pick it, got %s %v", res, err)
	}
	if _, err := b.Do(context.Background(), Call{Username: "alice", Browser: "laptop", Op: "tabs.list"}); err == nil ||
		!strings.Contains(err.Error(), "home, work") {
		t.Fatalf("an unknown name should list the connected ones, got %v", err)
	}
}

func TestAnExtensionErrorComesBackAsARemoteError(t *testing.T) {
	b, url := newBridge(t)
	x, _, _ := pairAndConnect(t, b, url, "alice", "home")
	defer x.conn.Close()
	x.setHandler(tabsHandler)
	_, err := b.Do(context.Background(), Call{Username: "alice", Op: "page.nonsense"})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Op != "page.nonsense" {
		t.Fatalf("expected a RemoteError for the op, got %v", err)
	}
}

func TestAWrongProtocolVersionIsRefused(t *testing.T) {
	_, url := newBridge(t)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.WriteJSON(frame{T: "hello", V: 99, PairCode: "x"})
	var reply frame
	if err := conn.ReadJSON(&reply); err != nil || reply.Code != "version" {
		t.Fatalf("expected a version error, got %+v %v", reply, err)
	}
}
