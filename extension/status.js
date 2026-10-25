// Shared by the popup and the options page: how a status reads to a person.

export const TITLES = {
  connected: 'Connected',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  paused: 'Paused',
  unpaired: 'Not paired',
  auth_failed: 'Pairing needed',
  error: 'Error',
  idle: 'Starting…',
}

export function since(ms) {
  if (!ms) return ''
  const s = Math.round((Date.now() - ms) / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.round(s / 60)}m`
  return `${Math.round(s / 3600)}h`
}

// render fills a .status block: a dot, a title, and a line of detail.
export function render(el, st) {
  if (!el || !st) return
  el.querySelector('.dot').dataset.s = st.status
  el.querySelector('.status-title').textContent = TITLES[st.status] || st.status
  const parts = []
  if (st.status === 'connected') {
    if (st.rttMs != null) parts.push(`ping ${st.rttMs}ms`)
    if (st.connectedAt) parts.push(`for ${since(st.connectedAt)}`)
  }
  if (st.detail) parts.push(st.detail)
  el.querySelector('.status-detail').textContent = parts.join(' · ')
}

export async function getStatus() {
  try {
    return await chrome.runtime.sendMessage({ type: 'getStatus' })
  } catch {
    return null
  }
}

export function onStatus(fn) {
  chrome.runtime.onMessage.addListener((msg) => {
    if (msg?.type === 'status') fn(msg)
  })
}
