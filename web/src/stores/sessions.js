import { defineStore } from 'pinia'
import { ref } from 'vue'

/** Reactive state for sessions and messages. No logic, no API calls. */
export const useSessionsStore = defineStore('sessions', () => {
  const sessionId = ref(localStorage.getItem('kaiju_session') || null)
  const sessions = ref([])
  const messages = ref([])
  const loading = ref(false)
  const intent = ref('operate')
  const runMode = ref(localStorage.getItem('kaiju_run_mode') || 'reflect')
  const aggMode = ref(localStorage.getItem('kaiju_agg_mode') || '-1')

  function setSessionId(id) {
    sessionId.value = id
    if (id) localStorage.setItem('kaiju_session', id)
    else localStorage.removeItem('kaiju_session')
  }

  function setRunMode(mode) {
    runMode.value = mode
    localStorage.setItem('kaiju_run_mode', mode)
  }

  function setAggMode(mode) {
    aggMode.value = mode
    localStorage.setItem('kaiju_agg_mode', mode)
  }

  return { sessionId, sessions, messages, loading, intent, runMode, aggMode, setRunMode, setAggMode, setSessionId }
})
