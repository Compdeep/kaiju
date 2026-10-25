// Kaiju Browser Relay — the service worker.
//
// Dials the Kaiju server, proves who it is, and then carries out the
// operations Kaiju asks for. The protocol is described in
// internal/browserbridge/protocol.go; the two must agree on PROTOCOL.
//
// Staying connected is most of this file. A Manifest V3 service worker is
// stopped by Chrome when it is idle, a laptop sleeps, Kaiju restarts. So:
//   - a heartbeat every 20s, which also keeps the worker alive (Chrome 116+
//     counts websocket traffic as activity);
//   - any frame from the server counts as life; 45s of silence and the socket
//     is closed and redialled, rather than trusted until a request hangs on it;
//   - reconnects back off 1s, 2s, 4s … 30s with jitter, and never give up;
//   - an alarm every 30s redials if the worker was stopped while it waited;
//   - an auth failure or a revocation is the one thing NOT retried: the
//     credential will never work, and retrying would only hammer the server.

const PROTOCOL = 1
const DEFAULT_SERVER = 'ws://127.0.0.1:8080/api/v1/browser/ws'
const HEARTBEAT_MS = 20_000
const SILENCE_MS = 45_000
const HELLO_TIMEOUT_MS = 10_000
const BACKOFF_MAX_MS = 30_000
const ALARM = 'kaiju-keepalive'

// Close codes from the server. See internal/browserbridge/protocol.go.
const CLOSE = { AUTH: 4001, PROTOCOL: 4002, TIMEOUT: 4008, REPLACED: 4009, REVOKED: 4010 }

const state = {
  ws: null,
  status: 'idle', // idle | unpaired | connecting | connected | reconnecting | paused | auth_failed | error
  detail: '',
  attempt: 0,
  retryTimer: null,
  retryAt: 0,
  heartbeat: null,
  lastFrameAt: 0,
  pingSeq: 0,
  pingSentAt: 0,
  rttMs: null,
  connectedAt: 0,
  deviceId: '',
  serverError: '',
}

// ─── Settings ───────────────────────────────────────────────────────────────

function defaultName() {
  const p = navigator.userAgentData?.platform || navigator.platform || 'desktop'
  return `Chrome on ${p}`
}

async function settings() {
  return chrome.storage.local.get({
    server: DEFAULT_SERVER,
    name: '',
    deviceId: '',
    secret: '',
    pairCode: '',
    paused: false,
  })
}

// ─── Status ─────────────────────────────────────────────────────────────────

const BADGE = {
  connected: { text: 'ON', color: '#16a34a' },
  connecting: { text: '…', color: '#2563eb' },
  reconnecting: { text: '…', color: '#d97706' },
  auth_failed: { text: '!', color: '#dc2626' },
  error: { text: '!', color: '#dc2626' },
  unpaired: { text: 'OFF', color: '#6b7280' },
  paused: { text: 'OFF', color: '#6b7280' },
  idle: { text: '', color: '#6b7280' },
}

function snapshot() {
  return {
    status: state.status,
    detail: state.detail,
    rttMs: state.rttMs,
    connectedAt: state.connectedAt,
    deviceId: state.deviceId,
    retryAt: state.retryAt,
    attempt: state.attempt,
    version: chrome.runtime.getManifest().version,
  }
}

function setStatus(status, detail = '') {
  state.status = status
  state.detail = detail
  const b = BADGE[status] || BADGE.idle
  chrome.action.setBadgeText({ text: b.text }).catch(() => {})
  chrome.action.setBadgeBackgroundColor({ color: b.color }).catch(() => {})
  chrome.action.setTitle({ title: `Kaiju Browser Relay — ${status}${detail ? ': ' + detail : ''}` }).catch(() => {})
  // Tell an open popup or options page; nobody listening is fine.
  chrome.runtime.sendMessage({ type: 'status', ...snapshot() }).catch(() => {})
}

// ─── Connection ─────────────────────────────────────────────────────────────

function isLive(ws) {
  return ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)
}

async function connect(reason) {
  if (isLive(state.ws)) return
  clearTimeout(state.retryTimer)
  state.retryTimer = null
  state.retryAt = 0

  const s = await settings()
  if (s.paused) return setStatus('paused', 'Paused from the popup')
  if (!s.secret && !s.pairCode) return setStatus('unpaired', 'Open Options and paste a pairing code from Kaiju')
  if (isLive(state.ws)) return // another call got here while settings loaded

  setStatus(state.attempt > 0 ? 'reconnecting' : 'connecting', s.server)
  let ws
  try {
    ws = new WebSocket(s.server)
  } catch (err) {
    setStatus('error', `Server address is not valid: ${err.message}`)
    return
  }
  state.ws = ws
  state.serverError = ''
  let welcomed = false

  const helloTimer = setTimeout(() => {
    if (state.ws === ws && !welcomed) ws.close(4000, 'no welcome')
  }, HELLO_TIMEOUT_MS)

  ws.onopen = () => {
    const hello = {
      t: 'hello',
      v: PROTOCOL,
      name: s.name || defaultName(),
      version: chrome.runtime.getManifest().version,
      user_agent: navigator.userAgent,
    }
    if (s.secret) {
      hello.device_id = s.deviceId
      hello.secret = s.secret
    } else {
      hello.pair_code = s.pairCode
    }
    ws.send(JSON.stringify(hello))
  }

  ws.onmessage = (ev) => {
    if (state.ws !== ws) return
    state.lastFrameAt = Date.now()
    let f
    try {
      f = JSON.parse(String(ev.data))
    } catch {
      return
    }
    if (!welcomed) {
      if (f.t === 'welcome') {
        welcomed = true
        clearTimeout(helloTimer)
        void onWelcome(f)
      } else if (f.t === 'error') {
        state.serverError = f.message || f.code || 'refused'
      }
      return
    }
    void onFrame(ws, f)
  }

  ws.onerror = () => {} // a close always follows, and that is where it is handled

  ws.onclose = (ev) => {
    clearTimeout(helloTimer)
    if (state.ws !== ws) return // a newer socket already replaced this one
    void onClosed(ev, welcomed, s)
  }
  console.log(`[kaiju] connecting (${reason}) to ${s.server}`)
}

async function onWelcome(f) {
  // The first welcome after pairing carries the device secret. It is the only
  // time the server sends it; the pairing code is used up and is forgotten.
  if (f.secret) {
    await chrome.storage.local.set({ deviceId: f.device_id, secret: f.secret, pairCode: '' })
  }
  state.deviceId = f.device_id
  state.attempt = 0
  state.connectedAt = Date.now()
  state.rttMs = null
  startHeartbeat()
  setStatus('connected', f.name || '')
  void sendEvent('connected', { tab_count: await tabCount() })
}

async function onClosed(ev, welcomed, s) {
  stopHeartbeat()
  state.ws = null
  state.connectedAt = 0
  const why = state.serverError || ev.reason || `closed (${ev.code})`

  if (ev.code === CLOSE.AUTH) {
    // A bad pairing code is forgotten so it is not sent again; a bad secret
    // likewise. Either way a new code is the only way back in.
    await chrome.storage.local.set(s.secret ? { secret: '', deviceId: '' } : { pairCode: '' })
    return setStatus('auth_failed', why)
  }
  if (ev.code === CLOSE.REVOKED) {
    await chrome.storage.local.set({ secret: '', deviceId: '' })
    return setStatus('auth_failed', 'This browser was revoked in Kaiju. Create a new pairing code to connect it again.')
  }
  if (ev.code === CLOSE.PROTOCOL) {
    // Usually a version mismatch. Retrying will not fix it quickly, but an
    // upgrade of either side will, so keep trying at the slowest pace.
    state.attempt = Math.max(state.attempt, 6)
  }
  scheduleRetry(welcomed ? `connection lost: ${why}` : `could not connect: ${why}`)
}

function scheduleRetry(why) {
  state.attempt += 1
  const base = Math.min(BACKOFF_MAX_MS, 1000 * 2 ** (state.attempt - 1))
  const delay = Math.round(base * (0.8 + Math.random() * 0.4))
  state.retryAt = Date.now() + delay
  clearTimeout(state.retryTimer)
  state.retryTimer = setTimeout(() => void connect('retry'), delay)
  setStatus('reconnecting', `${why} — retrying in ${Math.ceil(delay / 1000)}s`)
}

function reconnectNow(reason) {
  state.attempt = 0
  const ws = state.ws
  state.ws = null
  stopHeartbeat()
  if (ws && ws.readyState <= WebSocket.OPEN) ws.close(1000, reason)
  void connect(reason)
}

// ─── Heartbeat ──────────────────────────────────────────────────────────────

function startHeartbeat() {
  stopHeartbeat()
  state.heartbeat = setInterval(() => {
    const ws = state.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (Date.now() - state.lastFrameAt > SILENCE_MS) {
      // The socket says open but nothing has come back: a sleeping laptop or
      // a dead network path. Close it so the retry loop takes over.
      ws.close(4008, 'server silent')
      return
    }
    state.pingSeq += 1
    state.pingSentAt = Date.now()
    send({ t: 'ping', seq: state.pingSeq })
  }, HEARTBEAT_MS)
}

function stopHeartbeat() {
  clearInterval(state.heartbeat)
  state.heartbeat = null
}

function send(frame) {
  const ws = state.ws
  if (!ws || ws.readyState !== WebSocket.OPEN) return false
  ws.send(JSON.stringify(frame))
  return true
}

// ─── Frames ─────────────────────────────────────────────────────────────────

async function onFrame(ws, f) {
  switch (f.t) {
    case 'ping':
      send({ t: 'pong', seq: f.seq })
      return
    case 'pong':
      if (f.seq === state.pingSeq && state.pingSentAt) {
        state.rttMs = Date.now() - state.pingSentAt
        chrome.runtime.sendMessage({ type: 'status', ...snapshot() }).catch(() => {})
      }
      return
    case 'req':
      return handleRequest(ws, f)
    case 'error':
      console.warn('[kaiju] server error:', f.message || f)
      return
  }
}

async function handleRequest(ws, f) {
  const op = OPS[f.op]
  let reply
  if (!op) {
    reply = { t: 'res', id: f.id, ok: false, error: `this extension (v${chrome.runtime.getManifest().version}) does not know the operation "${f.op}" — it may need updating` }
  } else {
    try {
      const result = await op(f.args || {})
      reply = { t: 'res', id: f.id, ok: true, result }
    } catch (err) {
      reply = { t: 'res', id: f.id, ok: false, error: err?.message || String(err) }
    }
  }
  // Answer on the socket the request came in on. If it has gone, the server
  // already failed the request; sending on a newer socket would answer an id
  // that socket never issued.
  if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(reply))
}

// ─── Operations ─────────────────────────────────────────────────────────────
//
// Each takes the request's args and returns its result, or throws with a
// message that reaches the agent as the reason. New operations are added here;
// the server names them in tools/browser*.go.

function tabView(t) {
  return {
    id: t.id,
    window_id: t.windowId,
    index: t.index,
    active: !!t.active,
    pinned: !!t.pinned,
    audible: !!t.audible,
    status: t.status || '',
    title: t.title || '',
    url: t.url || t.pendingUrl || '',
  }
}

async function tabCount() {
  try {
    return (await chrome.tabs.query({})).length
  } catch {
    return 0
  }
}

const OPS = {
  async 'tabs.list'() {
    const tabs = await chrome.tabs.query({})
    tabs.sort((a, b) => a.windowId - b.windowId || a.index - b.index)
    const s = await settings()
    return { browser: s.name || defaultName(), tabs: tabs.map(tabView) }
  },

  async 'browser.info'() {
    const s = await settings()
    const windows = await chrome.windows.getAll()
    return {
      name: s.name || defaultName(),
      version: chrome.runtime.getManifest().version,
      user_agent: navigator.userAgent,
      windows: windows.length,
      tabs: await tabCount(),
    }
  },

  async 'tabs.open'(args) {
    const url = checkUrl(args.url || 'about:blank')
    const tab = await chrome.tabs.create({ url, active: !!args.active })
    const load = args.wait === false ? { waited: false } : await waitForLoad(tab.id, args.timeout_ms)
    return { tab: tabView(await chrome.tabs.get(tab.id)), ...load }
  },

  async 'tabs.close'(args) {
    const tab = await resolveTab(args.tab, { needTab: true })
    await chrome.tabs.remove(tab.id)
    return { closed: tabView(tab) }
  },

  async 'tabs.focus'(args) {
    const tab = await resolveTab(args.tab, { needTab: true })
    await chrome.tabs.update(tab.id, { active: true })
    await chrome.windows.update(tab.windowId, { focused: true })
    return { tab: tabView(await chrome.tabs.get(tab.id)) }
  },

  async 'tab.navigate'(args) {
    const tab = await resolveTab(args.tab)
    const action = args.action || (args.url ? 'url' : '')
    // Listen before acting, so a fast load cannot finish before we are watching.
    const loaded = args.wait === false ? Promise.resolve({ waited: false }) : waitForLoad(tab.id, args.timeout_ms, true)
    switch (action) {
      case 'url':
        await chrome.tabs.update(tab.id, { url: checkUrl(args.url) })
        break
      case 'back':
      case 'forward':
        await stepHistory(tab, action === 'back' ? -1 : 1)
        break
      case 'reload':
        await chrome.tabs.reload(tab.id)
        break
      default:
        throw new Error('give a url, or an action of back, forward or reload')
    }
    const load = await loaded
    return { tab: tabView(await chrome.tabs.get(tab.id)), ...load }
  },

  async 'page.read'(args) {
    const tab = await resolveTab(args.tab)
    checkReadable(tab)
    let results
    try {
      results = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: readPage,
        args: [{
          text: args.text !== false,
          elements: args.elements !== false,
          offset: args.offset || 0,
          max_chars: args.max_chars || 12000,
          max_elements: args.max_elements || 150,
        }],
      })
    } catch (err) {
      throw new Error(`could not read tab ${tab.id} (${tab.url}): ${err.message}`)
    }
    const page = results?.[0]?.result
    if (!page) throw new Error(`tab ${tab.id} returned nothing — it may still be loading`)
    return { tab: tab.id, window_id: tab.windowId, ...page }
  },

  async 'page.screenshot'(args) {
    const tab = await resolveTab(args.tab)
    checkReadable(tab)
    // The visible tab of a window can be captured directly. A background tab
    // cannot, so it goes through the DevTools protocol, which Chrome marks
    // with its "being debugged" bar while it is attached.
    if (tab.active) {
      const url = await chrome.tabs.captureVisibleTab(tab.windowId, { format: 'png' })
      return { tab: tab.id, url: tab.url, title: tab.title, method: 'visible', format: 'png', data: url.slice(url.indexOf(',') + 1) }
    }
    const target = await debuggee(tab.id, 5000)
    const shot = await chrome.debugger.sendCommand(target, 'Page.captureScreenshot', { format: 'png' })
    return { tab: tab.id, url: tab.url, title: tab.title, method: 'debugger', format: 'png', data: shot.data }
  },

  // ─── Acting on a page ──────────────────────────────────────────────────────
  //
  // Mouse and keyboard go through the DevTools protocol, so the page receives
  // trusted events: the same a person's hand would make. A DOM .click() is
  // ignored by many sites and never opens a native control.

  async 'input.click'(args) {
    const tab = await inputTab(args.tab)
    const action = args.action || 'click'
    let point, target
    if (args.ref || args.selector) {
      target = await locate(tab.id, { ref: args.ref, selector: args.selector })
      if (target.disabled && action !== 'hover') throw new Error(`${describeTarget(target)} is disabled`)
      if (target.covered && !args.force && action !== 'hover') {
        throw new Error(`${describeTarget(target)} is covered by ${target.covered} — close or scroll past what is on top, or pass force to click anyway`)
      }
      point = { x: target.x, y: target.y }
    } else if (Number.isFinite(args.x) && Number.isFinite(args.y)) {
      point = { x: args.x, y: args.y }
    } else {
      throw new Error('say what to click: an element ref from browser_read, a CSS selector, or x and y')
    }

    const d = await debuggee(tab.id)
    const before = (await chrome.tabs.get(tab.id)).url
    const opened = watchNewTabs(tab.id)
    await mouse(d, 'mouseMoved', point)
    if (action !== 'hover') {
      const button = action === 'right' ? 'right' : args.button === 'middle' ? 'middle' : 'left'
      const clicks = action === 'double' ? 2 : 1
      for (let n = 1; n <= clicks; n++) {
        await mouse(d, 'mousePressed', point, { button, clickCount: n })
        await mouse(d, 'mouseReleased', point, { button, clickCount: n })
      }
    }
    const after = await settle(tab.id, before, args.wait !== false)
    return { action, point: { x: Math.round(point.x), y: Math.round(point.y) }, target: target && pick(target, 'role', 'name', 'tag'), ...after, opened_tab: await opened.stop() }
  },

  async 'input.type'(args) {
    const tab = await inputTab(args.tab)
    if (typeof args.text !== 'string') throw new Error('give the text to type')
    let target
    if (args.ref || args.selector) {
      target = await locate(tab.id, { ref: args.ref, selector: args.selector, focus: true })
      if (target.disabled) throw new Error(`${describeTarget(target)} is disabled`)
      if (target.editable === false) throw new Error(`${describeTarget(target)} is not something that takes typing`)
    }
    const d = await debuggee(tab.id)
    // locate already focused it from script. A click as well puts the caret
    // where a person's would be, which some rich editors need before they
    // take input — unless something covers the field, when the click would
    // land on that instead and focus has to do.
    if (target && !target.covered) {
      await mouse(d, 'mouseMoved', target)
      await mouse(d, 'mousePressed', target, { button: 'left', clickCount: 1 })
      await mouse(d, 'mouseReleased', target, { button: 'left', clickCount: 1 })
    }
    if (args.clear) {
      await pressKey(d, isMac() ? 'Meta+A' : 'Control+A')
      await pressKey(d, 'Backspace')
    }
    if (args.text) await chrome.debugger.sendCommand(d, 'Input.insertText', { text: args.text })
    const before = (await chrome.tabs.get(tab.id)).url
    if (args.submit) await pressKey(d, 'Enter')
    const value = await focusedValue(tab.id)
    const after = await settle(tab.id, before, !!args.submit && args.wait !== false)
    return { typed: args.text.length, target: target && pick(target, 'role', 'name', 'tag'), field: value, submitted: !!args.submit, ...after }
  },

  async 'input.key'(args) {
    const tab = await inputTab(args.tab)
    const keys = String(args.keys || '').trim()
    if (!keys) throw new Error('give the key to press, such as Enter, Tab, Escape or Control+A')
    const repeat = Math.min(Math.max(Number(args.repeat) || 1, 1), 50)
    const d = await debuggee(tab.id)
    const before = (await chrome.tabs.get(tab.id)).url
    for (let i = 0; i < repeat; i++) {
      for (const combo of keys.split(/\s+/)) await pressKey(d, combo)
    }
    const after = await settle(tab.id, before, args.wait !== false)
    return { pressed: keys, repeat, ...after }
  },

  async 'input.select'(args) {
    const tab = await inputTab(args.tab)
    if (!args.ref && !args.selector) throw new Error('say which dropdown, by its ref from browser_read')
    const [{ result }] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: selectOption,
      args: [{ ref: args.ref, selector: args.selector, option: String(args.option ?? '') }],
    })
    if (result.error) throw new Error(result.error)
    return result
  },

  async 'page.scroll'(args) {
    const tab = await resolveTab(args.tab)
    checkReadable(tab)
    const [{ result }] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: scrollPage,
      args: [{ ref: args.ref, direction: args.direction || 'down', amount: args.amount }],
    })
    if (result.error) throw new Error(result.error)
    // A page that scrolls an inner panel rather than the window does not move
    // for window.scrollBy. A wheel event at the middle of the screen reaches
    // whatever scrolls there, the way a person's mouse wheel would.
    if (!result.moved && !args.ref && (result.direction === 'up' || result.direction === 'down')) {
      const d = await debuggee(tab.id)
      const dy = (result.direction === 'up' ? -1 : 1) * (result.amount || 600)
      await chrome.debugger.sendCommand(d, 'Input.dispatchMouseEvent', {
        type: 'mouseWheel', x: result.viewport.width / 2, y: result.viewport.height / 2, deltaX: 0, deltaY: dy,
      })
      result.method = 'wheel'
    }
    return result
  },

  async 'page.wait'(args) {
    const tab = await resolveTab(args.tab)
    checkReadable(tab)
    if (!args.text && !args.selector) throw new Error('say what to wait for: text that should appear, or a CSS selector')
    const limit = Math.min(Math.max(Number(args.timeout_ms) || 10000, 250), 60000)
    const start = Date.now()
    for (;;) {
      let found = false
      try {
        const [{ result }] = await chrome.scripting.executeScript({
          target: { tabId: tab.id },
          func: (o) => {
            const has = o.selector ? !!document.querySelector(o.selector) : (document.body?.innerText || '').toLowerCase().includes(o.text.toLowerCase())
            return o.gone ? !has : has
          },
          args: [{ text: args.text || '', selector: args.selector || '', gone: !!args.gone }],
        })
        found = !!result
      } catch {
        // The page is between documents; try again on the next tick.
      }
      if (found) return { found: true, waited_ms: Date.now() - start }
      if (Date.now() - start >= limit) return { found: false, waited_ms: Date.now() - start }
      await new Promise((r) => setTimeout(r, 250))
    }
  },
}

// ─── The debugger ───────────────────────────────────────────────────────────
//
// One attachment per tab, kept while the agent is working and dropped after it
// has been idle a while, so Chrome's "being debugged" bar does not flicker on
// every click. If the person dismisses that bar, Chrome detaches — and that is
// taken as them saying stop: the tab is left alone for a few minutes.

const DEBUG_IDLE_MS = 30_000
const STOPPED_FOR_MS = 5 * 60_000
const attached = new Map() // tabId → idle timer
const stoppedByPerson = new Map() // tabId → until

async function debuggee(tabId, idleMs = DEBUG_IDLE_MS) {
  const until = stoppedByPerson.get(tabId)
  if (until && until > Date.now()) {
    throw new Error('the person dismissed Chrome\'s debugging bar on this tab, which stops Kaiju acting on it for a few minutes — ask them before trying again')
  }
  const target = { tabId }
  if (!attached.has(tabId)) {
    try {
      await chrome.debugger.attach(target, '1.3')
    } catch (err) {
      throw new Error(`could not attach to tab ${tabId}: ${err.message} (is DevTools open on it?)`)
    }
  }
  clearTimeout(attached.get(tabId))
  attached.set(tabId, setTimeout(() => {
    attached.delete(tabId)
    chrome.debugger.detach(target).catch(() => {})
  }, idleMs))
  return target
}

chrome.debugger.onDetach.addListener((source, reason) => {
  if (!source.tabId) return
  clearTimeout(attached.get(source.tabId))
  attached.delete(source.tabId)
  if (reason === 'canceled_by_user') stoppedByPerson.set(source.tabId, Date.now() + STOPPED_FOR_MS)
})

// inputTab is the tab an input goes to. Input needs the page on screen: Chrome
// does not lay out or hit-test a hidden tab the same way, so a click there can
// land on nothing. A background tab is brought to the front first, and that is
// reported, because it changes what the person sees.
async function inputTab(id) {
  const tab = await resolveTab(id)
  checkReadable(tab)
  if (!tab.active) {
    await chrome.tabs.update(tab.id, { active: true })
    await new Promise((r) => setTimeout(r, 150))
  }
  return chrome.tabs.get(tab.id)
}

function mouse(target, type, point, extra = {}) {
  return chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {
    type, x: point.x, y: point.y, button: 'none', clickCount: 0, ...extra,
  })
}

function isMac() {
  return /Mac/i.test(navigator.userAgentData?.platform || navigator.platform || '')
}

// Keys by the names people write them. Anything not here that is one
// character is typed as that character.
const KEYS = {
  Enter: { code: 'Enter', keyCode: 13, text: '\r' },
  Tab: { code: 'Tab', keyCode: 9 },
  Escape: { code: 'Escape', keyCode: 27 },
  Backspace: { code: 'Backspace', keyCode: 8 },
  Delete: { code: 'Delete', keyCode: 46 },
  Space: { key: ' ', code: 'Space', keyCode: 32, text: ' ' },
  ArrowUp: { code: 'ArrowUp', keyCode: 38 },
  ArrowDown: { code: 'ArrowDown', keyCode: 40 },
  ArrowLeft: { code: 'ArrowLeft', keyCode: 37 },
  ArrowRight: { code: 'ArrowRight', keyCode: 39 },
  Home: { code: 'Home', keyCode: 36 },
  End: { code: 'End', keyCode: 35 },
  PageUp: { code: 'PageUp', keyCode: 33 },
  PageDown: { code: 'PageDown', keyCode: 34 },
}
for (let i = 1; i <= 12; i++) KEYS[`F${i}`] = { code: `F${i}`, keyCode: 111 + i }
const ALIASES = { Return: 'Enter', Esc: 'Escape', Del: 'Delete', Up: 'ArrowUp', Down: 'ArrowDown', Left: 'ArrowLeft', Right: 'ArrowRight' }
const MODIFIERS = { Alt: 1, Control: 2, Ctrl: 2, Meta: 4, Cmd: 4, Command: 4, Shift: 8 }

async function pressKey(target, combo) {
  const parts = combo.split('+').filter(Boolean)
  const name = parts.pop()
  let modifiers = 0
  for (const m of parts) {
    if (!(m in MODIFIERS)) throw new Error(`"${m}" in "${combo}" is not a modifier — use Control, Shift, Alt or Meta`)
    modifiers |= MODIFIERS[m]
  }
  let k = KEYS[ALIASES[name] || name]
  let key = k ? (k.key || ALIASES[name] || name) : name
  if (!k) {
    if ([...name].length !== 1) throw new Error(`"${name}" is not a key this knows — use a single character or a name such as Enter, Tab, Escape, ArrowDown`)
    const upper = name.toUpperCase()
    const code = /[A-Z]/.test(upper) ? `Key${upper}` : /[0-9]/.test(name) ? `Digit${name}` : ''
    k = { code, keyCode: upper.charCodeAt(0), text: name }
    key = name
  }
  // Text only when no command modifier is held: Control+A selects, it does not type "a".
  const text = modifiers & (1 | 2 | 4) ? undefined : k.text
  const base = { key, code: k.code, windowsVirtualKeyCode: k.keyCode, nativeVirtualKeyCode: k.keyCode, modifiers }
  await chrome.debugger.sendCommand(target, 'Input.dispatchKeyEvent', { type: text ? 'keyDown' : 'rawKeyDown', ...base, text, unmodifiedText: text })
  await chrome.debugger.sendCommand(target, 'Input.dispatchKeyEvent', { type: 'keyUp', ...base })
}

// settle waits for what an input set off. A click that starts a navigation is
// waited out; one that does not returns after a beat, with the page's address
// so a change of page is visible either way.
async function settle(tabId, beforeUrl, wait) {
  await new Promise((r) => setTimeout(r, 350))
  let tab = await chrome.tabs.get(tabId).catch(() => null)
  if (!tab) return { tab_closed: true }
  let timedOut = false
  if (wait && tab.status === 'loading') {
    timedOut = (await waitForLoad(tabId, 15000)).timed_out
    tab = await chrome.tabs.get(tabId)
  }
  const navigated = tab.url !== beforeUrl
  return { url: tab.url, title: tab.title, navigated, timed_out: timedOut || undefined }
}

// watchNewTabs notices a tab the click opened (a link with target=_blank, a
// popup), so the agent learns its id instead of losing it.
function watchNewTabs(openerId) {
  let found = null
  const on = (t) => { if (t.openerTabId === openerId && !found) found = { id: t.id, url: t.pendingUrl || t.url || '' } }
  chrome.tabs.onCreated.addListener(on)
  return {
    // The address is often not set yet when the tab is created, so it is
    // looked up again when reporting.
    async stop() {
      chrome.tabs.onCreated.removeListener(on)
      if (!found) return undefined
      const t = await chrome.tabs.get(found.id).catch(() => null)
      return { id: found.id, url: (t && (t.pendingUrl || t.url)) || found.url }
    },
  }
}

function pick(o, ...keys) {
  const out = {}
  for (const k of keys) if (o[k] !== undefined) out[k] = o[k]
  return out
}

function describeTarget(t) {
  return `${t.role || t.tag} "${t.name || ''}"`.trim()
}

// locate finds an element by ref or selector, scrolls it to the middle of the
// screen, and reports where its centre is and what, if anything, sits on top
// of it there. Runs in the isolated world, where readPage left its refs.
async function locate(tabId, o) {
  const [{ result }] = await chrome.scripting.executeScript({ target: { tabId }, func: locateInPage, args: [o] })
  if (!result || result.error) throw new Error(result?.error || 'the page did not answer')
  return result
}

function locateInPage(o) {
  const K = globalThis.__kaiju
  const clean = (s, n) => String(s || '').replace(/\s+/g, ' ').trim().slice(0, n)
  const describe = (el) => {
    const label = el.getAttribute('aria-label') || el.innerText || el.getAttribute('title') || ''
    return `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${label ? ` "${clean(label, 40)}"` : ''}`
  }
  let el
  if (o.ref) {
    el = K && K.refs.get(o.ref)
    if (!el) return { error: `${o.ref} is not from the latest reading of this page — read the page again and use a ref from that` }
    if (!el.isConnected) return { error: `${o.ref} is no longer on the page; it changed since it was read — read the page again` }
  } else {
    try {
      el = document.querySelector(o.selector)
    } catch (e) {
      return { error: `"${o.selector}" is not a valid CSS selector: ${e.message}` }
    }
    if (!el) return { error: `nothing on the page matches ${o.selector}` }
  }
  el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' })
  const r = el.getBoundingClientRect()
  if (r.width < 1 || r.height < 1) return { error: `${describe(el)} has no size on screen — it is hidden` }
  const x = Math.min(Math.max(r.left + r.width / 2, 1), innerWidth - 1)
  const y = Math.min(Math.max(r.top + r.height / 2, 1), innerHeight - 1)
  const top = document.elementFromPoint(x, y)
  const covered = top && top !== el && !el.contains(top) && !top.contains(el) && !(el.labels && [...el.labels].some((l) => l.contains(top))) ? describe(top) : null
  if (o.focus) el.focus()
  const tag = el.tagName.toLowerCase()
  const editable = el.isContentEditable || tag === 'textarea' || (tag === 'input' && !['button', 'submit', 'reset', 'checkbox', 'radio', 'file', 'image', 'range', 'color'].includes((el.type || 'text').toLowerCase()))
  const type = (el.type || 'text').toLowerCase()
  const implicit = tag === 'a' ? 'link' : tag === 'button' || tag === 'summary' ? 'button' : tag === 'select' ? 'combobox'
    : tag === 'textarea' ? 'textbox' : tag === 'input' ? (['button', 'submit', 'reset', 'image'].includes(type) ? 'button' : ['checkbox', 'radio'].includes(type) ? type : 'textbox')
    : el.isContentEditable ? 'textbox' : ''
  return {
    x, y, covered, tag, editable,
    role: el.getAttribute('role') || implicit,
    name: clean(el.getAttribute('aria-label') || (el.labels && el.labels[0]?.innerText) || el.innerText || el.value || el.getAttribute('placeholder') || '', 80),
    disabled: !!el.disabled || el.getAttribute('aria-disabled') === 'true',
  }
}

// focusedValue reports what the focused field holds after typing, so the agent
// can check the text landed. A password field says only how long it is.
async function focusedValue(tabId) {
  const [{ result }] = await chrome.scripting.executeScript({
    target: { tabId },
    func: () => {
      const el = document.activeElement
      if (!el || el === document.body) return undefined
      const v = el.isContentEditable ? el.innerText : el.value
      if (v === undefined) return undefined
      if (el.type === 'password') return `(${v.length} characters)`
      return String(v).slice(0, 300)
    },
  })
  return result
}

function selectOption(o) {
  const K = globalThis.__kaiju
  let el = o.ref ? K && K.refs.get(o.ref) : document.querySelector(o.selector)
  if (!el || !el.isConnected) return { error: `${o.ref || o.selector} is not on the page as last read — read the page again` }
  if (!(el instanceof HTMLSelectElement)) return { error: `${o.ref || o.selector} is a ${el.tagName.toLowerCase()}, not a dropdown — click it instead` }
  const want = o.option.trim().toLowerCase()
  const opts = [...el.options]
  const match = opts.find((op) => op.value.toLowerCase() === want) ||
    opts.find((op) => op.text.trim().toLowerCase() === want) ||
    opts.find((op) => op.text.trim().toLowerCase().includes(want))
  if (!match) return { error: `no option matches "${o.option}"; the options are: ${opts.map((op) => op.text.trim()).join(' | ').slice(0, 400)}` }
  if (match.disabled) return { error: `the option "${match.text.trim()}" is disabled` }
  el.value = match.value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  el.dispatchEvent(new Event('change', { bubbles: true }))
  return { selected: match.text.trim(), value: match.value }
}

function scrollPage(o) {
  const K = globalThis.__kaiju
  const vp = () => ({ width: innerWidth, height: innerHeight })
  if (o.ref) {
    const el = K && K.refs.get(o.ref)
    if (!el || !el.isConnected) return { error: `${o.ref} is not on the page as last read — read the page again` }
    el.scrollIntoView({ block: 'center', behavior: 'instant' })
    return { moved: true, scroll_y: Math.round(scrollY), page_height: document.documentElement.scrollHeight, viewport: vp() }
  }
  const before = scrollY
  const amount = Number(o.amount) > 0 ? Number(o.amount) : Math.round(innerHeight * 0.85)
  switch (o.direction) {
    case 'up': scrollBy({ top: -amount, behavior: 'instant' }); break
    case 'down': scrollBy({ top: amount, behavior: 'instant' }); break
    case 'top': scrollTo({ top: 0, behavior: 'instant' }); break
    case 'bottom': scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }); break
    default: return { error: 'direction must be up, down, top or bottom' }
  }
  return {
    moved: scrollY !== before, direction: o.direction, amount,
    scroll_y: Math.round(scrollY), page_height: document.documentElement.scrollHeight,
    at_bottom: Math.ceil(scrollY + innerHeight) >= document.documentElement.scrollHeight, viewport: vp(),
  }
}

// ─── Operation helpers ──────────────────────────────────────────────────────

// resolveTab is the tab an operation acts on: the one named, or else the tab
// the person is looking at in the window they used last.
async function resolveTab(id, { needTab = false } = {}) {
  if (id !== undefined && id !== null && id !== '') {
    try {
      return await chrome.tabs.get(Number(id))
    } catch {
      throw new Error(`there is no tab ${id} — it may have been closed; list the tabs again`)
    }
  }
  if (needTab) throw new Error('say which tab, by its id from the tab list')
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true })
  if (!tab) throw new Error('no tab is active — name one by its id from the tab list')
  return tab
}

// checkUrl allows only addresses a person could type into the address bar to
// reach a site. javascript: and data: URLs run code in whatever page they land
// on, and chrome:// pages are the browser's own settings.
function checkUrl(raw) {
  let u
  try {
    u = new URL(String(raw))
  } catch {
    throw new Error(`"${raw}" is not a full URL — include https://`)
  }
  if (u.href === 'about:blank' || u.protocol === 'http:' || u.protocol === 'https:') return u.href
  throw new Error(`only http and https addresses can be opened, not ${u.protocol}`)
}

const UNREADABLE = [/^chrome:/, /^edge:/, /^about:(?!blank)/, /^chrome-extension:/, /^devtools:/, /^view-source:/,
  /^https:\/\/chromewebstore\.google\.com/, /^https:\/\/chrome\.google\.com\/webstore/]

function checkReadable(tab) {
  const url = tab.url || tab.pendingUrl || ''
  if (UNREADABLE.some((re) => re.test(url))) {
    throw new Error(`Chrome does not let extensions read or capture ${url.split('?')[0]}`)
  }
  if (tab.discarded) throw new Error(`tab ${tab.id} is asleep (discarded by Chrome) — focus or reload it first`)
}

// waitForLoad resolves when the tab finishes loading, or when the wait runs
// out — a page that never settles is reported, not waited on forever.
function waitForLoad(tabId, timeoutMs, freshNavigation = false) {
  const limit = Math.min(Math.max(Number(timeoutMs) || 20000, 1000), 60000)
  return new Promise((resolve) => {
    let sawLoading = !freshNavigation
    const done = (timedOut) => {
      clearTimeout(timer)
      chrome.tabs.onUpdated.removeListener(onUpdated)
      resolve({ waited: true, timed_out: timedOut })
    }
    const onUpdated = (id, change) => {
      if (id !== tabId) return
      if (change.status === 'loading') sawLoading = true
      if (change.status === 'complete' && sawLoading) done(false)
    }
    const timer = setTimeout(() => done(true), limit)
    chrome.tabs.onUpdated.addListener(onUpdated)
    if (!freshNavigation) {
      chrome.tabs.get(tabId).then((t) => { if (t.status === 'complete') done(false) }).catch(() => done(false))
    }
  })
}

// stepHistory goes one page back or forward in a tab.
//
// Chrome's own back button skips pages the person did not reach by a click or
// a keystroke — which is every page an agent opened — so chrome.tabs.goBack
// says there is nothing to go back to. The page's own history has no such rule,
// so that is the fallback; and if nothing starts loading, there really was no
// page to go to, and the caller is told so rather than left to wait.
async function stepHistory(tab, step) {
  const word = step < 0 ? 'earlier' : 'later'
  try {
    await (step < 0 ? chrome.tabs.goBack(tab.id) : chrome.tabs.goForward(tab.id))
    return
  } catch {}
  checkReadable(tab)
  const before = tab.url
  await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: (n) => history.go(n), args: [step] })
  for (let i = 0; i < 15; i++) {
    await new Promise((r) => setTimeout(r, 100))
    const now = await chrome.tabs.get(tab.id)
    if (now.status === 'loading' || now.url !== before) return
  }
  throw new Error(`there is no ${word} page in this tab's history`)
}

// readPage runs inside the tab, in the extension's isolated world: the page's
// own scripts cannot see it or what it keeps. It must be self-contained —
// executeScript sends the function's source, not its closure.
//
// Elements are numbered e1, e2 … and the elements themselves are kept in a map
// in that isolated world, so a later click names "e12" and gets exactly the
// element that was read — or is told the page has changed since. Nothing is
// written into the page's DOM.
function readPage(opts) {
  const K = (globalThis.__kaiju = globalThis.__kaiju || { seq: 0, refs: new Map() })
  // Only a reading that lists elements hands out new refs. A text-only read
  // leaves the last list standing, so "read the words, then click e3" works.
  if (opts.elements) {
    K.seq += 1
    K.refs = new Map()
  }
  const out = {
    url: location.href,
    title: document.title,
    snapshot: K.seq,
    viewport: {
      width: innerWidth,
      height: innerHeight,
      scroll_y: Math.round(scrollY),
      page_height: document.documentElement.scrollHeight,
    },
    frames: document.querySelectorAll('iframe').length,
  }
  const clean = (s, n) => String(s || '').replace(/\s+/g, ' ').trim().slice(0, n)

  if (opts.text) {
    // The page's main content where it marks one and it has substance;
    // otherwise the whole body, which carries navigation and footers too.
    const main = document.querySelector('main, [role="main"], article')
    const root = main && main.innerText.trim().length > 200 ? main : document.body
    const text = (root ? root.innerText : '').replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim()
    const offset = Math.max(0, opts.offset | 0)
    const max = Math.min(Math.max(opts.max_chars | 0, 500), 50000)
    out.text = text.slice(offset, offset + max)
    out.text_length = text.length
    out.text_offset = offset
    out.text_truncated = offset + max < text.length
    out.text_source = root === document.body ? 'body' : root.tagName.toLowerCase()
  }

  if (opts.elements) {
    const SEL = [
      'a[href]', 'button', 'input:not([type="hidden"])', 'select', 'textarea', 'summary',
      '[role="button"]', '[role="link"]', '[role="checkbox"]', '[role="radio"]', '[role="tab"]',
      '[role="menuitem"]', '[role="option"]', '[role="switch"]', '[role="combobox"]', '[role="textbox"]',
      '[role="searchbox"]', '[contenteditable=""]', '[contenteditable="true"]', '[onclick]',
    ].join(',')
    const implicitRole = (el) => {
      const tag = el.tagName.toLowerCase()
      if (tag === 'a') return 'link'
      if (tag === 'button' || tag === 'summary') return 'button'
      if (tag === 'select') return 'combobox'
      if (tag === 'textarea') return 'textbox'
      if (tag === 'input') {
        const t = (el.type || 'text').toLowerCase()
        if (['button', 'submit', 'reset', 'image'].includes(t)) return 'button'
        if (t === 'checkbox' || t === 'radio') return t
        if (t === 'range') return 'slider'
        return 'textbox'
      }
      if (el.isContentEditable) return 'textbox'
      return 'generic'
    }
    const nameOf = (el) => {
      const by = el.getAttribute('aria-labelledby')
      if (by) {
        const t = by.split(/\s+/).map((id) => document.getElementById(id)?.innerText || '').join(' ')
        if (t.trim()) return t
      }
      if (el.getAttribute('aria-label')) return el.getAttribute('aria-label')
      if (el.labels && el.labels.length) return el.labels[0].innerText
      const tag = el.tagName.toLowerCase()
      if (tag === 'input' && ['button', 'submit', 'reset'].includes(el.type)) return el.value
      const text = el.innerText
      if (text && text.trim()) return text
      return el.getAttribute('placeholder') || el.getAttribute('title') || el.querySelector('img[alt]')?.alt || el.getAttribute('name') || ''
    }
    const visible = (el, r) => {
      if (r.width < 1 || r.height < 1) return false
      const cs = getComputedStyle(el)
      return cs.visibility !== 'hidden' && cs.display !== 'none' && Number(cs.opacity) > 0
    }

    const found = []
    for (const el of document.querySelectorAll(SEL)) {
      const r = el.getBoundingClientRect()
      if (!visible(el, r)) continue
      const inView = r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth
      found.push({ el, r, inView })
    }
    // What is on screen first, then the rest in document order, so a cap
    // keeps what the person can see.
    found.sort((a, b) => (a.inView === b.inView ? 0 : a.inView ? -1 : 1))
    const max = Math.min(Math.max(opts.max_elements | 0, 10), 500)
    out.elements_total = found.length
    out.elements = found.slice(0, max).map(({ el, r, inView }, i) => {
      const ref = `e${i + 1}`
      K.refs.set(ref, el)
      const tag = el.tagName.toLowerCase()
      const e = { ref, role: el.getAttribute('role') || implicitRole(el), name: clean(nameOf(el), 100), tag }
      if (tag === 'input') {
        e.type = (el.type || 'text').toLowerCase()
        if (e.type === 'checkbox' || e.type === 'radio') e.checked = !!el.checked
        else if (e.type === 'password') e.value = el.value ? '(filled)' : ''
        else if (el.value) e.value = clean(el.value, 100)
      } else if (tag === 'textarea' || tag === 'select') {
        if (el.value) e.value = clean(el.value, 100)
      }
      if (tag === 'a' && el.href) e.href = el.href.slice(0, 200)
      if (el.disabled || el.getAttribute('aria-disabled') === 'true') e.disabled = true
      e.in_viewport = inView
      e.box = { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) }
      return e
    })
  }
  return out
}

// ─── Events to Kaiju ────────────────────────────────────────────────────────
//
// What the person does in the browser, so Kaiju knows which browser is in use
// and how many tabs it has. Kaiju does not cache the tab list from these; it
// asks for it fresh.

async function sendEvent(ev, data) {
  if (state.status !== 'connected') return
  send({ t: 'event', ev, data })
}

chrome.tabs.onActivated.addListener(async ({ tabId, windowId }) => {
  void sendEvent('tab.activated', { tab_id: tabId, window_id: windowId, tab_count: await tabCount() })
})
chrome.tabs.onCreated.addListener(async (tab) => {
  void sendEvent('tab.created', { tab: tabView(tab), tab_count: await tabCount() })
})
chrome.tabs.onRemoved.addListener(async (tabId, info) => {
  void sendEvent('tab.removed', { tab_id: tabId, window_id: info.windowId, tab_count: await tabCount() })
})
chrome.tabs.onUpdated.addListener((tabId, change, tab) => {
  if (change.status === 'complete' || change.title || change.url) void sendEvent('tab.updated', { tab: tabView(tab) })
})
chrome.windows.onFocusChanged.addListener((windowId) => {
  if (windowId !== chrome.windows.WINDOW_ID_NONE) void sendEvent('window.focused', { window_id: windowId })
})

// ─── Lifecycle ──────────────────────────────────────────────────────────────

chrome.runtime.onStartup.addListener(() => void connect('browser start'))

chrome.runtime.onInstalled.addListener(async () => {
  await chrome.alarms.create(ALARM, { periodInMinutes: 0.5 })
  const s = await settings()
  if (!s.secret && !s.pairCode) chrome.runtime.openOptionsPage()
  void connect('installed')
})

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== ALARM) return
  // The worker may have been stopped mid-backoff, taking its timer with it.
  // If nothing is connected or scheduled, dial now.
  if (!isLive(state.ws) && !state.retryTimer) void connect('keepalive alarm')
})

chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return
  if (changes.paused) {
    if (changes.paused.newValue) {
      const ws = state.ws
      state.ws = null
      stopHeartbeat()
      clearTimeout(state.retryTimer)
      state.retryTimer = null
      if (ws) ws.close(1000, 'paused')
      return setStatus('paused', 'Paused from the popup')
    }
    return reconnectNow('resumed')
  }
  // A new server, or a new pairing code pasted in Options: start over with it.
  const pairedNow = changes.secret?.newValue && !changes.secret?.oldValue
  if (changes.server || (changes.pairCode && changes.pairCode.newValue)) reconnectNow('settings changed')
  else if (changes.name && !pairedNow && state.status === 'connected') reconnectNow('renamed')
})

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  switch (msg?.type) {
    case 'getStatus':
      reply(snapshot())
      return
    case 'reconnect':
      reconnectNow('asked from the popup')
      reply(snapshot())
      return
  }
})

// Every time the worker starts — first install, browser start, or woken by an
// event after Chrome stopped it — make sure the alarm exists and dial.
chrome.alarms.get(ALARM).then((a) => a || chrome.alarms.create(ALARM, { periodInMinutes: 0.5 }))
void connect('worker start')
