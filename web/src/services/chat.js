import api from '../api/client'
import { useSessionsStore } from '../stores/sessions'
import { useDagStore } from '../stores/dag'

/** Chat service — session CRUD, send, interject. Writes to per-session stores. */

/**
 * desc: Build the [attached files] preamble that gets prepended to a user
 * query when uploads are attached. The format mirrors how the agent's
 * Executive expects to see file references — workspace-relative paths plus
 * sidecar metadata paths so it can read previews without burning a
 * file_read on the full original. Tiny files are inlined directly.
 * @param {Object[]} atts - attachment Result objects
 * @returns {string} block text, or '' if no attachments
 */
function buildAttachmentBlock(atts) {
  if (!atts || !atts.length) return ''
  const lines = ['[attached files]']
  for (const a of atts) {
    const parts = [`- ${a.path} (${a.type}, ${a.size} bytes`]
    if (a.lines) parts.push(`, ${a.lines} lines`)
    parts.push(')')
    if (a.meta_path) parts.push(`; preview: ${a.meta_path}`)
    if (a.summary_path) parts.push(`; summary: ${a.summary_path}`)
    lines.push(parts.join(''))
    if (a.inline) {
      lines.push('  inline content:')
      lines.push('  ```')
      lines.push(a.inline.split('\n').map(l => '  ' + l).join('\n'))
      lines.push('  ```')
    }
  }
  lines.push('')
  lines.push('[query]')
  return lines.join('\n')
}

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

  // Restore the chip strip from any existing uploads on the session
  // (best-effort — stay silent on failure).
  api.get(`/api/v1/sessions/${id}/uploads`).then(list => {
    if (Array.isArray(list)) s.attachments = list.map(a => ({ ...a, pending: false }))
  }).catch(() => {})

  // Load from server if this session hasn't been loaded yet
  const ss = s.getSession(id)
  if (ss.messages.length === 0) {
    try {
      const msgs = await api.get(`/api/v1/sessions/${id}/messages`)
      ss.messages = (msgs || []).map(m => {
        const msg = { id: m.id, role: m.role, content: m.content }
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
 * desc: Re-fetch a session's messages from the server (so they carry DB ids,
 *       needed for edit/regenerate). Replaces the session's message list.
 * @param {string} id - session id
 */
export async function refreshMessages(id) {
  const s = useSessionsStore()
  const ss = s.getSession(id)
  if (!ss) return
  try {
    const msgs = await api.get(`/api/v1/sessions/${id}/messages`)
    ss.messages = (msgs || []).map(m => {
      const msg = { id: m.id, role: m.role, content: m.content }
      if (m.dag_trace) { try { msg.trace = JSON.parse(m.dag_trace) } catch {} }
      return msg
    })
  } catch { /* keep current view on failure */ }
}

/**
 * desc: Edit one message's stored content (owner-only, enforced server-side).
 * @param {string} sid - session id
 * @param {number} msgId - message id
 * @param {string} content - new content
 */
export async function editMessage(sid, msgId, content) {
  await api.patch(`/api/v1/sessions/${sid}/messages/${msgId}`, { content })
}

/**
 * desc: Delete a message (and everything after it) from a session, then refresh.
 *       Owner-only, enforced server-side. Unsticks a turn with no reply.
 * @param {string} sid - session id
 * @param {number} msgId - message id
 */
export async function deleteMessage(sid, msgId) {
  const s = useSessionsStore()
  await api.del(`/api/v1/sessions/${sid}/messages/${msgId}`)
  const sess = s.getSession(sid)
  if (sess) sess.loading = false // deleting clears any stuck "inflight" state
  await refreshMessages(sid)
}

/**
 * desc: Regenerate the last turn — drop the last assistant reply and re-run the
 *       last user message on the server, then refresh the view.
 */
export async function regenerate() {
  const s = useSessionsStore()
  const dag = useDagStore()
  const sid = s.sessionId
  const sess = s.getSession(sid)
  if (!sess) return
  // Optimistically drop the old assistant reply NOW so the view shows just the
  // "thinking" state, not old-reply + new-reply stacked. refreshMessages() below
  // reconciles with the server (which also deleted it).
  if (sess.messages.length && sess.messages[sess.messages.length - 1].role === 'assistant') {
    sess.messages.pop()
  }
  sess.loading = true
  dag.archiveAndClear()
  try {
    await api.post('/api/v1/execute', {
      session_id: sid,
      regenerate: true,
      chat_mode: s.chatMode || undefined,
      mode: s.runMode,
      agg_mode: parseInt(s.aggMode),
      execution_mode: s.executionMode || undefined,
    })
  } catch (err) {
    console.error('regenerate:', err)
  } finally {
    sess.loading = false
    dag.archiveAndClear()
    await refreshMessages(sid)
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

  // Bind to the session that ORIGINATED this send. Every message/loading write
  // below happens AFTER an await, so it must target THIS session's state — not
  // the active-session proxies (s.messages/s.loading/s.sessionId), which follow
  // whatever chat is open now. Writing through the proxies is what made a reply
  // land in the wrong chat when you switched mid-request.
  const sendingSid = s.sessionId
  const sendingSess = s.getSession(sendingSid)
  if (!sendingSess) return

  // Snapshot attachments BEFORE we mutate the list — they're cleared
  // after the user message is built so subsequent queries start fresh.
  const attached = (sendingSess.attachments || []).filter(a => !a.pending && !a.error)
  const attachBlock = buildAttachmentBlock(attached)
  const queryWithAttachments = attachBlock ? `${attachBlock}\n${text}` : text

  sendingSess.messages.push({ role: 'user', content: text })
  sendingSess.loading = true
  // Mark the per-session state as actively sending so the SSE 'done'
  // handler doesn't fire its page-reload-recovery path during a normal
  // run (which would refetch messages before /trace lands and clobber
  // the in-memory msg.trace we're about to set below).
  sendingSess.sendInFlight = true
  dag.archiveAndClear()
  dag.interjectMode = true
  dag.interjections = []

  // Clear the chip strip — files stay on disk; the agent has the paths.
  sendingSess.attachments = []

  try {
    const data = await api.post('/api/v1/execute', {
      query: queryWithAttachments,
      session_id: sendingSid,
      intent: s.intent,
      // Chat is the front door: every turn runs the chat lane. The tuned
      // classifier decides whether it needs the agent (which streams its steps
      // into the DAG trace below) and which chat tools are available comes from
      // the instance config (Settings). The old direct-vs-agent toggle is gone.
      chat_mode: true,
    })
    const msg = {
      role: 'assistant',
      content: data.error ? `[error] ${data.error}` : (data.verdict || dag.streamingVerdict || 'No response'),
    }
    if (dag.nodes.length) msg.trace = [...dag.nodes]
    if (data.gaps && data.gaps.length) msg.gaps = data.gaps
    sendingSess.messages.push(msg)
    dag.streamingVerdict = ''
  } catch (err) {
    sendingSess.messages.push({ role: 'assistant', content: `[error] ${err.message}` })
  } finally {
    sendingSess.loading = false
    dag.interjectMode = false
    dag.interjections = []
    if (sendingSid && dag.nodes.length) {
      try { await api.post(`/api/v1/sessions/${sendingSid}/trace`, { nodes: dag.nodes }) } catch {}
    }
    // Trace is now persisted. Clear the in-flight flag *after* /trace
    // so any late-arriving SSE 'done' for this session sees us still
    // active and skips the recovery reload.
    sendingSess.sendInFlight = false
    dag.archiveAndClear()
    loadSessions()
    // Sync ids from the server so the just-sent messages become editable.
    refreshMessages(sendingSid)
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
