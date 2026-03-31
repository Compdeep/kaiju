import { useDagStore } from '../stores/dag'
import { usePanelStore } from '../stores/panel'

/**
 * Tools service — SSE connection and event routing.
 * Owns the EventSource. Writes DAG events to dag store, panel events to panel store.
 * Reads node.actions to route tool side-effects (panel_show, notify, etc).
 */

let eventSource = null

/**
 * desc: Process actions attached to a DAG node (panel_show, notify, etc) and dispatch them to the panel store
 * @param {Array<Object>} actions - List of action objects with type and payload fields
 * @returns {void}
 */
function handleActions(actions) {
  if (!actions || !actions.length) return
  const panel = usePanelStore()

  for (const action of actions) {
    switch (action.type) {
      case 'panel_show':
        panel.pushTab({
          plugin: action.plugin,
          title: action.title || action.plugin,
          path: action.path || null,
          content: action.content || null,
          mime: action.mime || null,
          line: action.line || 0,
        })
        break
      // Future: case 'notify', case 'navigate', etc.
    }
  }
}

/**
 * desc: Establish an SSE connection to /events and route incoming events to the DAG store
 * @returns {void}
 */
export function connect() {
  if (eventSource) return

  eventSource = new EventSource('/events')

  eventSource.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data)
      const dag = useDagStore()

      switch (ev.type) {
        case 'start':
          dag.nodes = []
          dag.streamingVerdict = ''
          dag.running = true
          dag.interjectMode = true
          dag.interjections = []
          break

        case 'verdict':
          if (ev.text) dag.streamingVerdict += ev.text
          break

        case 'node':
          if (ev.node) {
            const idx = dag.nodes.findIndex(n => n.id === ev.node.id)
            if (idx >= 0) dag.nodes[idx] = { ...ev.node }
            else dag.nodes.push({ ...ev.node })
            // Route any actions attached to this node
            if (ev.node.actions) handleActions(ev.node.actions)
          }
          break

        case 'done': {
          const final = (ev.nodes || []).map(n => ({ ...n }))
          for (const fn of final) {
            const idx = dag.nodes.findIndex(n => n.id === fn.id)
            if (idx >= 0) dag.nodes[idx] = fn
            else dag.nodes.push(fn)
            // Route actions from final snapshot too
            if (fn.actions) handleActions(fn.actions)
          }
          dag.running = false
          dag.interjectMode = false
          break
        }
      }
    } catch {}
  }
}

/**
 * desc: Close the SSE connection and clean up the EventSource instance
 * @returns {void}
 */
export function disconnect() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}
