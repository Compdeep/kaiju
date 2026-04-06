import api from '../api/client'
import { useSessionsStore } from '../stores/sessions'
import { useDagStore } from '../stores/dag'

/** Chat service — session CRUD, send, interject. Writes to stores. */

/**
 * desc: Create a new chat session on the server and reset local state
 * @returns {Promise<string>} The new session ID
 */
export async function createSession() {
  const s = useSessionsStore()
  const data = await api.post('/api/v1/sessions', { channel: 'web' })
  s.setSessionId(data.id)
  s.messages = []
  useDagStore().reset()
  await loadSessions()
  return data.id
}

/**
 * desc: Fetch all sessions from the server and update the sessions store
 * @returns {Promise<void>}
 */
export async function loadSessions() {
  const s = useSessionsStore()
  try { s.sessions = await api.get('/api/v1/sessions') } catch {}
}

/**
 * desc: Switch to an existing session by loading its messages and parsing any stored DAG traces
 * @param {string} id - The session ID to switch to
 * @returns {Promise<void>}
 */
export async function switchSession(id) {
  const s = useSessionsStore()
  const dag = useDagStore()
  s.setSessionId(id)
  dag.reset()
  try {
    const msgs = await api.get(`/api/v1/sessions/${id}/messages`)
    s.messages = (msgs || []).map(m => {
      const msg = { role: m.role, content: m.content }
      if (m.dag_trace) {
        try { msg.trace = JSON.parse(m.dag_trace) } catch {}
      }
      return msg
    })
  } catch {
    s.messages = []
  }
}

/**
 * desc: Delete a session from the server and clear local state if it was the active session
 * @param {string} id - The session ID to delete
 * @returns {Promise<void>}
 */
export async function deleteSession(id) {
  const s = useSessionsStore()
  const dag = useDagStore()
  try { await api.del(`/api/v1/sessions/${id}`) } catch {}
  if (s.sessionId === id) {
    s.setSessionId(null)
    s.messages = []
    dag.reset()
  }
  await loadSessions()
}

/**
 * desc: Compact the current session's message history on the server and reload it
 * @returns {Promise<void>}
 */
export async function compactSession() {
  const s = useSessionsStore()
  if (!s.sessionId) return
  try {
    await api.post(`/api/v1/sessions/${s.sessionId}/compact`)
    await switchSession(s.sessionId)
  } catch (err) { console.error('compact failed:', err) }
}

/**
 * desc: Send a user message to the DAG execution engine, creating a session if needed, and append the assistant response
 * @param {string} text - The user's message text
 * @returns {Promise<void>}
 */
export async function send(text) {
  const s = useSessionsStore()
  const dag = useDagStore()

  if (!s.sessionId) await createSession()

  s.messages.push({ role: 'user', content: text })
  s.loading = true
  dag.nodes = []
  dag.interjectMode = true
  dag.interjections = []

  try {
    const data = await api.post('/api/v1/execute', {
      query: text,
      session_id: s.sessionId,
      intent: s.intent,
      mode: s.runMode,
      agg_mode: parseInt(s.aggMode),
      execution_mode: s.executionMode || undefined,
    })
    const msg = {
      role: 'assistant',
      content: data.error ? `[error] ${data.error}` : (data.verdict || dag.streamingVerdict || 'No response'),
    }
    if (dag.nodes.length) msg.trace = [...dag.nodes]
    if (data.gaps && data.gaps.length) msg.gaps = data.gaps
    s.messages.push(msg)
    dag.streamingVerdict = ''
  } catch (err) {
    s.messages.push({ role: 'assistant', content: `[error] ${err.message}` })
  } finally {
    s.loading = false
    dag.interjectMode = false
    dag.interjections = []
    if (s.sessionId && dag.nodes.length) {
      try { await api.post(`/api/v1/sessions/${s.sessionId}/trace`, { nodes: dag.nodes }) } catch {}
    }
    dag.nodes = []
    loadSessions()
  }
}

/**
 * desc: Send an interjection message into a currently running DAG execution
 * @param {string} text - The interjection text to inject
 * @returns {Promise<boolean>} Whether the interjection was delivered
 */
export async function interject(text) {
  const dag = useDagStore()
  try {
    const data = await api.post('/api/v1/interject', { message: text })
    if (data.sent) {
      const truncated = text.length > 40 ? text.slice(0, 40) + '\u2026' : text
      dag.interjections.push({ text, truncated })
      dag.nodes.push({ id: 'interject-' + Date.now(), type: 'interjection', state: 'running', tag: truncated, tool: '' })
    } else {
      dag.interjections.push({ text, truncated: '(not delivered \u2014 no active query)' })
    }
    return data.sent
  } catch { return false }
}
