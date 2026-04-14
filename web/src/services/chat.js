import api from '../api/client'
import { useSessionsStore } from '../stores/sessions'
import { useDagStore } from '../stores/dag'

/** Chat service — session CRUD, send, interject. Writes to per-session stores. */

/**
 * desc: Create a new chat session on the server and switch to it
 * @returns {Promise<string>} The new session ID
 */
export async function createSession() {
  const s = useSessionsStore()
  const dag = useDagStore()
  const data = await api.post('/api/v1/sessions', { channel: 'web' })
  s.setSessionId(data.id)
  dag.setActiveSession(data.id)
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
 * desc: Switch to an existing session. Loads messages from server if not cached.
 *       Does NOT clear the old session's state — it stays in the per-session map.
 * @param {string} id - The session ID to switch to
 * @returns {Promise<void>}
 */
export async function switchSession(id) {
  const s = useSessionsStore()
  const dag = useDagStore()

  s.setSessionId(id)
  dag.setActiveSession(id)

  // Load from server if this session hasn't been loaded yet
  const ss = s.getSession(id)
  if (ss.messages.length === 0) {
    try {
      const msgs = await api.get(`/api/v1/sessions/${id}/messages`)
      ss.messages = (msgs || []).map(m => {
        const msg = { role: m.role, content: m.content }
        if (m.dag_trace) {
          try { msg.trace = JSON.parse(m.dag_trace) } catch {}
        }
        return msg
      })
      // Detect inflight query: last message is user with no assistant reply
      if (ss.messages.length > 0 && ss.messages[ss.messages.length - 1].role === 'user') {
        ss.loading = true
        const ds = dag.getSession(id)
        if (ds) {
          ds.running = true
          ds.interjectMode = true
        }
      }
    } catch {
      ss.messages = []
    }
  }
}

/**
 * desc: Delete a session from the server and clean up per-session state
 * @param {string} id - The session ID to delete
 * @returns {Promise<void>}
 */
export async function deleteSession(id) {
  const s = useSessionsStore()
  const dag = useDagStore()
  try { await api.del(`/api/v1/sessions/${id}`) } catch {}
  s.dropSession(id)
  dag.dropSession(id)
  if (s.sessionId === id) {
    s.setSessionId(null)
    dag.setActiveSession(null)
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
    // Force reload from server by clearing cached messages first
    const ss = s.getSession(s.sessionId)
    if (ss) ss.messages = []
    await switchSession(s.sessionId)
  } catch (err) { console.error('compact failed:', err) }
}

/**
 * desc: Send a user message to the DAG execution engine
 * @param {string} text - The user's message text
 * @returns {Promise<void>}
 */
export async function send(text) {
  const s = useSessionsStore()
  const dag = useDagStore()

  if (!s.sessionId) await createSession()

  s.messages.push({ role: 'user', content: text })
  s.loading = true
  dag.archiveAndClear()
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
    dag.archiveAndClear()
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
      const maxNum = dag.nodes.reduce((m, n) => {
        const num = parseInt((n.id || '').replace(/\D/g, '')) || 0
        return num > m ? num : m
      }, 0)
      dag.nodes.push({ id: `inj${maxNum + 1}`, type: 'interjection', state: 'running', tag: truncated, tool: '' })
    } else {
      dag.interjections.push({ text, truncated: '(not delivered \u2014 no active query)' })
    }
    return data.sent
  } catch { return false }
}
