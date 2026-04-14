import { defineStore } from 'pinia'
import { ref, reactive, computed } from 'vue'

/** Per-session DAG execution trace. Each session gets its own state slot. */
export const useDagStore = defineStore('dag', () => {
  const activeSessionId = ref(null)
  const sessions = reactive(new Map())

  function _ensure(sid) {
    if (!sid) return null
    if (!sessions.has(sid)) {
      sessions.set(sid, reactive({
        nodes: [],
        previousNodes: [],
        running: false,
        streamingVerdict: '',
        interjectMode: false,
        interjections: [],
      }))
    }
    return sessions.get(sid)
  }

  function _active() { return _ensure(activeSessionId.value) }

  // Public API — same ref shape as before, proxied to active session.
  const nodes = computed({
    get: () => _active()?.nodes || [],
    set: (v) => { if (_active()) _active().nodes = v }
  })
  const previousNodes = computed({
    get: () => _active()?.previousNodes || [],
    set: (v) => { if (_active()) _active().previousNodes = v }
  })
  const running = computed({
    get: () => _active()?.running || false,
    set: (v) => { if (_active()) _active().running = v }
  })
  const streamingVerdict = computed({
    get: () => _active()?.streamingVerdict || '',
    set: (v) => { if (_active()) _active().streamingVerdict = v }
  })
  const interjectMode = computed({
    get: () => _active()?.interjectMode || false,
    set: (v) => { if (_active()) _active().interjectMode = v }
  })
  const interjections = computed({
    get: () => _active()?.interjections || [],
    set: (v) => { if (_active()) _active().interjections = v }
  })

  /** Get a specific session's reactive state (for SSE routing). */
  function getSession(sid) { return _ensure(sid) }

  /** Switch which session the computed refs point to. */
  function setActiveSession(sid) {
    _ensure(sid)
    activeSessionId.value = sid
  }

  /** Archive current nodes before a new run. */
  function archiveAndClear(sid) {
    const s = sid ? _ensure(sid) : _active()
    if (!s) return
    if (s.nodes.length > 0) s.previousNodes = [...s.nodes]
    s.nodes = []
    s.streamingVerdict = ''
  }

  /** Reset all state for a session. */
  function reset(sid) {
    const s = sid ? _ensure(sid) : _active()
    if (!s) return
    if (s.nodes.length > 0) s.previousNodes = [...s.nodes]
    s.nodes = []
    s.streamingVerdict = ''
    s.running = false
    s.interjectMode = false
    s.interjections = []
  }

  /** Clean up on session delete. */
  function dropSession(sid) { sessions.delete(sid) }

  return {
    activeSessionId, nodes, previousNodes, running, streamingVerdict,
    interjectMode, interjections,
    getSession, setActiveSession, archiveAndClear, reset, dropSession,
  }
})
