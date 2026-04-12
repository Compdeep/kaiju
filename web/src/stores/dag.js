import { defineStore } from 'pinia'
import { ref } from 'vue'

/** Reactive state for DAG execution trace. No logic, no SSE. */
export const useDagStore = defineStore('dag', () => {
  const nodes = ref([])
  const previousNodes = ref([])
  const running = ref(false)
  const streamingVerdict = ref('')
  const interjectMode = ref(false)
  const interjections = ref([])

  /**
   * desc: Archive current nodes before clearing so the trace is never lost.
   *       Called before starting a new run.
   */
  function archiveAndClear() {
    if (nodes.value.length > 0) {
      previousNodes.value = [...nodes.value]
    }
    nodes.value = []
    streamingVerdict.value = ''
  }

  /**
   * desc: Reset all DAG state to initial values (nodes, verdict, running flag, interjections)
   * @returns {void}
   */
  function reset() {
    if (nodes.value.length > 0) {
      previousNodes.value = [...nodes.value]
    }
    nodes.value = []
    streamingVerdict.value = ''
    running.value = false
    interjectMode.value = false
    interjections.value = []
  }

  return { nodes, previousNodes, running, streamingVerdict, interjectMode, interjections, archiveAndClear, reset }
})
