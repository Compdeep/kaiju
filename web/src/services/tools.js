import { useDagStore } from '../stores/dag'
import { useSessionsStore } from '../stores/sessions'
import { usePanelStore } from '../stores/panel'
import api from '../api/client'

/**
 * Tools service — SSE connection and event routing.
 * Owns the EventSource. Routes events to the correct per-session state slot.
 */

let eventSource = null

// Run id -> session id, for events that arrive without a session_id.
//
// Keyed by run, not by the caller's trigger id: one trigger id can produce
// several runs — a retried piece of work carries the id it was given — and
// keying by it drew the second run's graph into the first run's session.
const runToSession = new Map()

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
 * Priority: event.session_id > runToSession mapping > active session (fallback).
 */
function resolveSession(ev) {
  if (ev.session_id) return ev.session_id
  if (ev.run_id) {
    const mapped = runToSession.get(ev.run_id)
    if (mapped) return mapped
  }
  return useSessionsStore().sessionId
}

/**
 * Read back the viewed session's state after the stream was interrupted.
 *
 * The server ends the response when it had to drop a state event, because a
 * node's terminal state exists nowhere else and a trace left showing the old one
 * stays wrong for the rest of the session. This is the half that makes the
 * reconnect worth anything: what was missed is read back rather than waited for.
 *
 * Only the session on screen, and only while it still believes it is running —
 * that spinner is exactly what a dropped terminal event leaves behind, and the
 * stored messages carry the answer and the finished trace it would have brought.
 */
function resync() {
  const dag = useDagStore()
  const sessions = useSessionsStore()

  const sid = dag.activeSessionId || sessions.sessionId
  if (!sid) return
  const ds = dag.getSession(sid)
  if (!ds || !ds.running) return

  const ss = sessions.getSession(sid)
  if (ss && ss.sendInFlight) return // the POST in flight will deliver it itself

  api.get(`/api/v1/sessions/${sid}/messages`).then(msgs => {
    const list = msgs || []
    const last = list.length ? list[list.length - 1] : null
    // An assistant reply is on record, so the run this trace is waiting on is
    // over, however its last event ended up.
    if (!last || last.role !== 'assistant') return
    for (const n of ds.nodes) if (n.state === 'running') n.state = 'resolved'
    ds.running = false
    ds.interjectMode = false
    if (ss) {
      ss.loading = false
      ss.messages = list.map(m => {
        const msg = { role: m.role, content: m.content }
        if (m.dag_trace) {
          try { msg.trace = JSON.parse(m.dag_trace) } catch {}
        }
        return msg
      })
    }
  }).catch(() => {})
}

export function connect() {
  if (eventSource) return

  // /events is JWT-authenticated and per-principal filtered; an EventSource
  // can't set headers, so the token rides the query string (kaiju supports it).
  const token = localStorage.getItem('kaiju_token') || ''
  eventSource = new EventSource('/events?token=' + encodeURIComponent(token))

  // Resync whenever the stream (re)opens.
  //
  // The server ends the response when it had to drop a state event, because a
  // node's terminal state exists nowhere else and a trace showing the old one
  // stays wrong for the rest of the session. EventSource reconnects on its own,
  // and this is the half that makes that worth doing: whatever was missed while
  // we were away is read back from the server rather than waited for.
  //
  // `first` skips the very first open, where there is nothing to have missed and
  // send() is usually mid-flight.
  let first = true
  eventSource.onopen = () => {
    if (first) { first = false; return }
    resync()
  }

  eventSource.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data)
      const dag = useDagStore()
      const sessions = useSessionsStore()
      const sid = resolveSession(ev)

      switch (ev.type) {
        case 'start':
          if (ev.run_id && sid) runToSession.set(ev.run_id, sid)
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

        // The engine calls this 'outcome' — chat.go, aggregator.go, scheduler.go
        // and loop_react.go all broadcast that name. This listened for
        // 'verdict', which nothing sends, so no chunk ever arrived and the
        // reply appeared in one piece when the POST returned.
        //
        // Appending is safe for both shapes the event carries: a streaming
        // stage sends many chunks, a non-streaming one sends a single whole
        // text, and no session gets both.
        case 'outcome':
        case 'verdict':
          {
            const ds = dag.getSession(sid)
            if (ds && ev.text) ds.streamingVerdict += ev.text
          }
          break

        case 'reasoning':
          {
            const ds = dag.getSession(sid)
            if (ds && ev.text) ds.streamingReasoning += ev.text
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
            // The run is over, so nothing is still working. A stage that only
            // ever exists as an event — the aggregator, the chat node — has no
            // entry in the final snapshot to correct it, and its terminal event
            // shares a 64-deep subscriber channel with the answer it streams
            // just beforehand, so that event is the one dropped when the buffer
            // is full. The trace then persists with `synthesize` spinning.
            for (const n of ds.nodes) if (n.state === 'running') n.state = 'resolved'
            ds.running = false
            ds.interjectMode = false
          }
          // If this session was reloaded mid-query (loading=true, no
          // active send() in progress), refetch messages so the stored
          // verdict lands in the UI. The `sendInFlight` flag is set by
          // chat.js#send() for the lifetime of a normal POST/execute
          // call, including the follow-up /trace persist. We only
          // recover when that flag is false — otherwise the in-flight
          // send() is about to push the assistant message itself, and
          // a reload here would clobber it (and beat /trace to the
          // server, dropping the trace from the rendered message).
          const ss = sessions.getSession(sid)
          if (ss && ss.loading && !ss.sendInFlight) {
            const lastMsg = ss.messages.length ? ss.messages[ss.messages.length - 1] : null
            if (lastMsg && lastMsg.role === 'user') {
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
          }
          // Clean up mapping
          if (ev.run_id) runToSession.delete(ev.run_id)
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
