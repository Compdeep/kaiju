import { render, getStatus, onStatus } from './status.js'

const statusEl = document.getElementById('status')
const pauseBtn = document.getElementById('pause')
const metaEl = document.getElementById('meta')

async function refresh(st) {
  st = st || (await getStatus())
  render(statusEl, st)
  const { paused, server, name } = await chrome.storage.local.get({ paused: false, server: '', name: '' })
  pauseBtn.textContent = paused ? 'Resume' : 'Pause'
  metaEl.textContent = [name, server, st?.version && `v${st.version}`].filter(Boolean).join(' · ')
}

document.getElementById('reconnect').addEventListener('click', async () => {
  render(statusEl, await chrome.runtime.sendMessage({ type: 'reconnect' }))
})

// Pause is the kill switch: it closes the socket and stops every reconnect
// until it is resumed. Stored, so it survives a browser restart.
pauseBtn.addEventListener('click', async () => {
  const { paused } = await chrome.storage.local.get({ paused: false })
  await chrome.storage.local.set({ paused: !paused })
  setTimeout(refresh, 100)
})

document.getElementById('options').addEventListener('click', () => chrome.runtime.openOptionsPage())

onStatus((st) => void refresh(st))
void refresh()
