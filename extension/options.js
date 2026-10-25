import { render, getStatus, onStatus } from './status.js'

const DEFAULT_SERVER = 'ws://127.0.0.1:8080/api/v1/browser/ws'
const $ = (id) => document.getElementById(id)

function flash(id, text) {
  $(id).textContent = text
  setTimeout(() => ($(id).textContent = ''), 3000)
}

function validServer(url) {
  try {
    const u = new URL(url)
    return u.protocol === 'ws:' || u.protocol === 'wss:'
  } catch {
    return false
  }
}

async function load() {
  const s = await chrome.storage.local.get({ server: DEFAULT_SERVER, name: '', deviceId: '', secret: '' })
  $('server').value = s.server
  $('name').value = s.name
  $('pairing').textContent = s.secret
    ? `Paired as device ${s.deviceId}.`
    : 'Not paired yet.'
  render($('status'), await getStatus())
}

$('pair').addEventListener('click', async () => {
  const code = $('pairCode').value.trim()
  if (!/^kjp-[a-z0-9]+$/i.test(code)) {
    flash('pairMsg', 'That does not look like a Kaiju pairing code (kjp-…).')
    return
  }
  const server = $('server').value.trim() || DEFAULT_SERVER
  if (!validServer(server)) {
    flash('pairMsg', 'The server must start with ws:// or wss://')
    return
  }
  // A new code replaces whatever pairing this browser had. The service worker
  // sees the change and connects with it at once.
  await chrome.storage.local.set({ server, pairCode: code, secret: '', deviceId: '', paused: false })
  $('pairCode').value = ''
  flash('pairMsg', 'Pairing…')
  void load()
})

$('save').addEventListener('click', async () => {
  const server = $('server').value.trim() || DEFAULT_SERVER
  if (!validServer(server)) {
    flash('saveMsg', 'The server must start with ws:// or wss://')
    return
  }
  await chrome.storage.local.set({ server, name: $('name').value.trim() })
  flash('saveMsg', 'Saved')
})

$('forget').addEventListener('click', async () => {
  await chrome.storage.local.set({ secret: '', deviceId: '', pairCode: '' })
  await chrome.runtime.sendMessage({ type: 'reconnect' }).catch(() => {})
  void load()
})

onStatus((st) => {
  render($('status'), st)
  if (st.status === 'connected') void load()
})
void load()
