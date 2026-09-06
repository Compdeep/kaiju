import { defineStore } from 'pinia'
import { ref, reactive, computed } from 'vue'

/** Per-session messages and loading state. Session list and settings are global. */
export const useSessionsStore = defineStore('sessions', () => {
  const sessionId = ref(localStorage.getItem('kaiju_session') || null)
  const sessions = ref([])
  const intent = ref('')
  const runMode = ref(localStorage.getItem('kaiju_run_mode') || 'reflect')
  const aggMode = ref(localStorage.getItem('kaiju_agg_mode') || '-1')
  // How a turn is handled: 'chat', 'auto' or 'agent'. One setting, because it
  // used to be two — a chat toggle and an execution mode — and the second was
  // ignored whenever the first was on, so two of their four combinations were
  // the same and nothing said so.
  //
  // Reads the two old keys once so a browser that remembers the old pair keeps
  // the behaviour it had: the chat toggle wins where it was on, since it was the
  // one that decided the lane.
  const executionMode = ref(readMode())
  function readMode() {
    const current = localStorage.getItem('kaiju_exec_mode')
    if (current === 'chat' || current === 'auto' || current === 'agent') return current
    if (localStorage.getItem('kaiju_chat_mode') === '1') return 'chat'
    return current === 'autonomous' ? 'agent' : 'auto'
  }

  // Per-session state: messages + loading
  const perSession = reactive(new Map())

  function _ensure(sid) {
    if (!sid) return null
    if (!perSession.has(sid)) {
      perSession.set(sid, reactive({ messages: [], loading: false, attachments: [], sendInFlight: false }))
    }
    return perSession.get(sid)
  }

  // Proxied accessors for the active session
  const messages = computed({
    get: () => _ensure(sessionId.value)?.messages || [],
    set: (v) => { const s = _ensure(sessionId.value); if (s) s.messages = v }
  })
  const loading = computed({
    get: () => _ensure(sessionId.value)?.loading || false,
    set: (v) => { const s = _ensure(sessionId.value); if (s) s.loading = v }
  })
  // Per-session list of attached uploads. Each entry is the upload
  // Result returned by POST /api/v1/sessions/<sid>/uploads (or a
  // `pending: true` placeholder while uploading). Cleared by chat.send()
  // after the user's message is dispatched.
  const attachments = computed({
    get: () => _ensure(sessionId.value)?.attachments || [],
    set: (v) => { const s = _ensure(sessionId.value); if (s) s.attachments = v }
  })

  /** Get a specific session's state (for SSE routing). */
  function getSession(sid) { return _ensure(sid) }

  function setSessionId(id) {
    sessionId.value = id
    if (id) {
      localStorage.setItem('kaiju_session', id)
      _ensure(id)
    } else {
      localStorage.removeItem('kaiju_session')
    }
  }

  function setRunMode(mode) {
    runMode.value = mode
    localStorage.setItem('kaiju_run_mode', mode)
  }

  function setAggMode(mode) {
    aggMode.value = mode
    localStorage.setItem('kaiju_agg_mode', mode)
  }

  function setExecutionMode(mode) {
    executionMode.value = mode
    localStorage.setItem('kaiju_exec_mode', mode)
  }

  /** Clean up on session delete. */
  function dropSession(sid) { perSession.delete(sid) }

  return {
    sessionId, sessions, messages, loading, attachments, intent,
    runMode, aggMode, executionMode,
    setRunMode, setAggMode, setExecutionMode, setSessionId,
    getSession, dropSession,
  }
})
