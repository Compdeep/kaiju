import { defineStore } from 'pinia'
import { ref } from 'vue'

/** Reactive state for DAG execution trace. No logic, no SSE. */
export const useDagStore = defineStore('dag', () => {
  const nodes = ref([])
  const running = ref(false)
  const streamingVerdict = ref('')
  const interjectMode = ref(false)
  const interjections = ref([])

  /**
   * desc: Reset all DAG state to initial values (nodes, verdict, running flag, interjections)
   * @returns {void}
   */
  function reset() {
    nodes.value = []
    streamingVerdict.value = ''
    running.value = false
    interjectMode.value = false
    interjections.value = []
  }

  return { nodes, running, streamingVerdict, interjectMode, interjections, reset }
})
