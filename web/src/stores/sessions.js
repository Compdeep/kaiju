import { defineStore } from 'pinia'
import { ref, reactive, computed } from 'vue'

/** Per-session messages and loading state. Session list and settings are global. */
export const useSessionsStore = defineStore('sessions', () => {
  const sessionId = ref(localStorage.getItem('kaiju_session') || null)
  const sessions = ref([])
  const intent = ref('')
  const runMode = ref(localStorage.getItem('kaiju_run_mode') || 'reflect')
  const aggMode = ref(localStorage.getItem('kaiju_agg_mode') || '-1')
  const executionMode = ref(localStorage.getItem('kaiju_exec_mode') || 'interactive')

  // Per-session state: messages + loading
  const perSession = reactive(new Map())

  function _ensure(sid) {
    if (!sid) return null
    if (!perSession.has(sid)) {
      perSession.set(sid, reactive({ messages: [], loading: false }))
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
    sessionId, sessions, messages, loading, intent,
    runMode, aggMode, executionMode,
    setRunMode, setAggMode, setExecutionMode, setSessionId,
    getSession, dropSession,
  }
})
