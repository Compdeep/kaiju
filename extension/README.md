# Kaiju Browser Relay

A Chrome extension that connects your own browser to Kaiju, so the agent can see and work in your tabs when you ask it to. No Playwright and no separate Chromium: it uses the Chrome you already have, with your sign-ins.

## Install (unpacked)

1. Open `chrome://extensions` and turn on **Developer mode**.
2. **Load unpacked** → choose this `extension/` folder.
3. The options page opens. Leave the server as `ws://127.0.0.1:8080/api/v1/browser/ws` for a Kaiju on this machine.

## Pair

1. In a Kaiju chat, ask it to **connect my browser**. It replies with a code starting `kjp-`. Kaiju also serves this at `POST /api/v1/browser/pair`.
2. Paste the code into the extension's options page and press **Pair**.
3. The badge turns green **ON**, and the popup says **Connected** with the ping time.

The code works once and expires after 10 minutes. On first use the extension trades it for a device secret that only the extension keeps (Kaiju stores a hash of it). So the code left in the chat is useless once it has been used.

## Staying connected

- Heartbeat every 20s. 45s with nothing heard from Kaiju and the socket is redialled.
- Reconnects back off 1s → 30s and never give up. A `chrome.alarms` wake-up every 30s redials if Chrome stopped the service worker in between.
- After a revocation, or a pairing code or secret Kaiju refuses, it stops and asks you to pair again rather than retrying forever.
- **Pause** in the popup is the kill switch. It disconnects and stays disconnected, across browser restarts, until you resume.

## Protocol

JSON frames over one websocket, described in `internal/browserbridge/protocol.go`. Kaiju asks for operations by name and the extension carries them out. Operations live in `OPS` in `background.js`.

| Operation | Does | Kaiju tool |
|---|---|---|
| `tabs.list` | every tab in every window | `browser_tabs` |
| `tabs.open` / `tabs.close` / `tabs.focus` | open a URL (background by default), close a tab, bring one to the front | `browser_tab` |
| `tab.navigate` | load a URL, or back / forward / reload, then wait for the load | `browser_navigate` |
| `page.read` | the page's text (from `main` when it has one) and a numbered list of links, buttons and fields | `browser_read` |
| `page.screenshot` | a PNG of the tab: captured directly if it is on screen, through the debugger if it is in the background | `browser_screenshot` |
| `input.click` | click, double-click, right-click or hover, by element ref, CSS selector or point | `browser_click` |
| `input.type` | type into a field (optionally clearing it first and pressing Enter after) | `browser_type` |
| `input.key` | press keys and shortcuts: `Enter`, `Escape`, `Control+A` … | `browser_key` |
| `input.select` | choose an option in a `<select>` | `browser_select` |
| `page.scroll` | scroll the page, or bring an element into view | `browser_scroll` |
| `page.wait` | wait for text or an element to appear or disappear | `browser_wait` |
| `browser.info` | name, version, window and tab counts | — |

### How input works

- **Trusted events.** Mouse and keyboard go through Chrome's DevTools protocol, so the page gets trusted events (`event.isTrusted` is true), the same as a real hand. Typing uses `Input.insertText`, which fires the input events that React and similar frameworks listen for.
- **One debugger session per tab.** It stays attached while the agent is working and detaches after 30 seconds idle, so Chrome's "being debugged" bar doesn't flicker on every action. If you dismiss that bar, the extension leaves that tab alone for five minutes.
- **Checks before every click.** The element is scrolled to the middle of the screen. If something else is on top of it there (a cookie banner, a dialog), the click is refused, naming what covers it. A disabled element is refused too.
- **Stale refs are refused.** A ref that is no longer on the page, or comes from an earlier reading, is refused rather than guessed at. Only a reading that lists elements hands out new refs; a text-only read keeps the old ones.
- **Background tabs come to the front.** Input needs the page on screen, so a background tab is brought to the front before input.
- **Results say what changed.** Each result reports whether the page navigated, and any tab the click opened.

`page.read` runs in the extension's isolated world. Element refs (`e1`, `e2` …) are kept there, mapped to the elements themselves, and nothing is written into the page. A password field reports only whether it is filled.

Chrome's own pages (`chrome://`, the Web Store, other extensions) cannot be read or captured. The operations say so.

Capturing a background tab attaches Chrome's debugger for the moment it takes, so Chrome briefly shows its "being debugged" bar on that tab.

## Permissions

`tabs`, `storage`, `alarms`, plus `scripting`, `debugger` and access to all sites. The last three are what reading pages and capturing background tabs need. After updating from 0.1, press the reload icon on the extension in `chrome://extensions`.

## Revoke

`DELETE /api/v1/browser/devices/{id}` drops the browser at once and refuses its secret from then on. **Forget pairing** in the options page only clears the secret from this browser.
