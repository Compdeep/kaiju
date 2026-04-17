import { useDagStore } from '../stores/dag'
import { useSessionsStore } from '../stores/sessions'
import { usePanelStore } from '../stores/panel'
import api from '../api/client'

/**
 * Tools service — SSE connection and event routing.
 * Owns the EventSource. Routes events to the correct per-session state slot.
 */

let eventSource = null

// AlertID -> SessionID mapping for events that may lack session_id
const alertToSession = new Map()

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
    }
  }
}

/**
 * Resolve which session an SSE event belongs to.
 * Priority: event.session_id > alertToSession mapping > active session (fallback).
 */
function resolveSession(ev) {
  if (ev.session_id) return ev.session_id
  if (ev.alert) {
    const mapped = alertToSession.get(ev.alert)
    if (mapped) return mapped
  }
  return useSessionsStore().sessionId
}

export function connect() {
  if (eventSource) return

  eventSource = new EventSource('/events')

  eventSource.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data)
      const dag = useDagStore()
      const sessions = useSessionsStore()
      const sid = resolveSession(ev)

      switch (ev.type) {
        case 'start':
          if (ev.alert && sid) alertToSession.set(ev.alert, sid)
          dag.archiveAndClear(sid)
          {
            const ds = dag.getSession(sid)
            if (ds) {
              ds.running = true
              ds.interjectMode = true
              ds.interjections = []
            }
          }
          break

        case 'verdict':
          {
            const ds = dag.getSession(sid)
            if (ds && ev.text) ds.streamingVerdict += ev.text
          }
          break

        case 'node':
          if (ev.node) {
            const ds = dag.getSession(sid)
            if (ds) {
              let idx = ds.nodes.findIndex(n => n.id === ev.node.id)
              if (idx < 0 && ev.node.type === 'interjection') {
                idx = ds.nodes.findIndex(n => n.id.startsWith('inj') && n.type === 'interjection')
              }
              if (idx >= 0) ds.nodes[idx] = { ...ev.node }
              else ds.nodes.push({ ...ev.node })
              if (ev.node.actions) handleActions(ev.node.actions)
            }
          }
          break

        case 'done': {
          const ds = dag.getSession(sid)
          if (ds) {
            const final = (ev.nodes || []).map(n => ({ ...n }))
            for (const fn of final) {
              const idx = ds.nodes.findIndex(n => n.id === fn.id)
              if (idx >= 0) ds.nodes[idx] = fn
              else ds.nodes.push(fn)
              if (fn.actions) handleActions(fn.actions)
            }
            ds.running = false
            ds.interjectMode = false
          }
          // If this session was detected as inflight on page reload
          // (loading=true but no active send() in progress), reload
          // messages to pick up the stored verdict. We detect "no active
          // send()" by checking if the POST hasn't added the assistant
          // message yet — if the last message is still 'user', we're in
          // the recovery path and should reload.
          const ss = sessions.getSession(sid)
          if (ss && ss.loading) {
            const lastMsg = ss.messages.length ? ss.messages[ss.messages.length - 1] : null
            if (lastMsg && lastMsg.role === 'user') {
              // Recovery: page was reloaded mid-query. Reload messages from server.
              ss.loading = false
              api.get(`/api/v1/sessions/${sid}/messages`).then(msgs => {
                ss.messages = (msgs || []).map(m => {
                  const msg = { role: m.role, content: m.content }
                  if (m.dag_trace) {
                    try { msg.trace = JSON.parse(m.dag_trace) } catch {}
                  }
                  return msg
                })
              }).catch(() => {})
            }
            // Otherwise send() is still in flight and will handle the message itself.
          }
          // Clean up mapping
          if (ev.alert) alertToSession.delete(ev.alert)
          break
        }
      }
    } catch {}
  }
}

export function disconnect() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}
