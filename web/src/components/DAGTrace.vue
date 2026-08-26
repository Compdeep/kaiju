<template>
  <div :class="['trace', { collapsed: !expanded, live: running }]">
    <div class="trace-header" @click="expanded = !expanded">
      <span class="h-chr">{{ expanded ? '─' : '+' }}</span>
      <span class="h-title">trace</span>
      <span class="h-sep">──</span>
      <span class="h-val">{{ nodes.length }}</span>
      <span class="h-dim">nodes</span>
      <span class="h-sep">·</span>
      <span class="h-val">{{ totalMs }}</span>
      <span class="h-dim">ms</span>
      <span v-if="totalTokens > 0" class="h-sep">·</span>
      <span v-if="totalTokens > 0" class="h-val">{{ fmtTokens(totalTokens) }}</span>
      <span v-if="totalTokens > 0" class="h-dim">tok</span>
      <span :class="['h-status', status]">{{ statusLabel }}</span>
      <!-- Live activity ticker: visible while running whether or not the trace is
           expanded. The spinner + elapsed clock keep MOVING even when a single
           slow step (a thinking model, a heavy fetch) emits nothing, so a live run
           never looks frozen. The activity text names the current step. -->
      <template v-if="running">
        <span class="h-spin">{{ spinner }}</span>
        <span class="h-activity">{{ activity }}</span>
        <span class="h-elapsed">{{ liveElapsed }}</span>
      </template>
      <span v-else-if="!expanded && latestTag" class="h-latest">{{ latestTag }}</span>
    </div>

    <transition name="expand">
      <div v-if="expanded" class="trace-body">
        <template v-for="item in layout" :key="item.key">

          <div v-if="item.type === 'node'"
               class="tl tl-node tl-clickable"
               :style="indent(item.depth)"
               @click="toggleResult(item.node.id)">
            <span v-if="item.depth" class="t-branch">{{ item.last ? '└' : '├' }}</span>
            <span :class="['t-rail', item.node.type, item.node.state]"></span>
            <span :class="['t-ty', item.node.type, { 'is-skill': item.node.source === 'skillmd' }]">{{ item.node.source === 'skillmd' ? 'SKI' : tyLabel(item.node.type) }}</span>
            <span :class="['t-name', { 'is-skill': item.node.source === 'skillmd' }]">{{ item.node.type === 'tool' ? (item.node.tool || item.node.tag || item.node.id) : (item.node.tag || item.node.tool || item.node.id) }}</span>
            <span v-if="item.node.retry" class="t-retry">{{ item.node.retry }} retry</span>
            <span v-if="item.node.params" class="t-params">{{ compactParams(item.node.params) }}</span>
            <span v-if="item.node.summary" class="t-summary">{{ item.node.summary }}</span>
            <span class="t-gap"></span>
            <span v-if="item.node.ms" class="t-ms">{{ item.node.ms }}ms</span>
            <span v-if="item.node.tokens_in || item.node.tokens_out" class="t-tokens">{{ fmtTokens((item.node.tokens_in || 0) + (item.node.tokens_out || 0)) }}tok</span>
            <span v-if="item.node.result_size" class="t-size">{{ fmtSize(item.node.result_size) }}</span>
            <span :class="['t-st', item.node.state]">{{ stChr(item.node.state) }}</span>
            <span v-if="item.node.result || item.node.payload" class="t-expand">{{ expandedResults[item.node.id] ? '−' : '+' }}</span>
          </div>

          <div v-if="item.type === 'node' && (item.node.result || item.node.payload) && expandedResults[item.node.id]"
               :class="['tl-result', item.node.type]" :style="indent(item.depth + 1)">
            <div v-if="item.node.type === 'holmes' && parseReasoning(item.node.result)" class="t-rca-content">
              <div class="t-rca-reasoning">{{ parseReasoning(item.node.result) }}</div>
              <div v-if="parseRCA(item.node.result)" class="t-rca-conclusion">
                <span class="t-rca-label">Root cause:</span> {{ parseRCA(item.node.result) }}
              </div>
            </div>
            <template v-else>
              <!-- The tool's own fields, every one of them. The text below is
                   cut at 512 chars, so a tool returning one long value showed
                   the start of that value and none of its other fields. -->
              <pre v-if="item.node.payload" class="t-code t-payload"><span
                v-for="(p, pi) in jsonParts(fmtPayload(item.node.payload))" :key="pi" :class="'j-' + p.c">{{ p.t }}</span></pre>
              <pre v-if="resultText(item.node)" class="t-code"><span
                v-for="(p, pi) in jsonParts(resultText(item.node))" :key="pi" :class="'j-' + p.c">{{ p.t }}</span></pre>
            </template>
          </div>

          <!-- What the planner was shown. A plan that reached for the wrong
               tool and a plan never shown the right one read the same from
               outside; this is the difference. -->
          <div v-if="item.type === 'tools'" class="tl tl-sub tl-clickable"
               :style="indent(item.depth)" @click="toggleResult('tools-' + item.node.id)">
            <span class="t-branch">└</span>
            <span class="t-sub-label">saw</span>
            <span class="t-sub-val">{{ item.node.tools.length }}</span>
            <span class="t-sub-label">tools</span>
            <span v-if="!expandedResults['tools-' + item.node.id]" class="t-tool-peek">{{ item.node.tools.slice(0, 4).join(' · ') }}{{ item.node.tools.length > 4 ? ' …' : '' }}</span>
            <span class="t-gap"></span>
            <span class="t-expand">{{ expandedResults['tools-' + item.node.id] ? '−' : '+' }}</span>
          </div>

          <div v-if="item.type === 'tools' && expandedResults['tools-' + item.node.id]"
               class="tl-result executive" :style="indent(item.depth + 1)">
            <div class="t-chips">
              <span v-for="(t, ti) in item.node.tools" :key="t"
                    :class="['t-tool-chip', { lead: ti === 0 }]">{{ t }}</span>
            </div>
            <div v-if="item.node.objective" class="t-objective">
              <span class="t-objective-label">ranked against</span>
              <span class="t-objective-text">{{ item.node.objective }}</span>
            </div>
          </div>

          <div v-if="item.type === 'interject'" class="tl tl-sub" :style="indent(item.depth)">
            <span class="t-branch">└</span>
            <span class="t-interject-label">you asked</span>
            <span class="t-interject-msg">"{{ item.msg }}"</span>
          </div>

          <div v-if="item.type === 'spawn'" class="tl tl-sub" :style="indent(item.depth)">
            <span class="t-branch">└</span>
            <span class="t-spawn">spawned by {{ item.name }}</span>
          </div>

          <div v-if="item.type === 'skills'" class="tl tl-sub" :style="indent(item.depth)">
            <span class="t-branch">└</span>
            <span class="t-sub-label">guided by</span>
            <span v-for="sk in item.skills" :key="sk" class="t-skill-chip">{{ sk }}</span>
          </div>

          <div v-if="item.type === 'error'" class="tl tl-sub tl-err tl-clickable"
               :style="indent(item.depth)" @click="toggleResult('err-' + item.key)">
            <span class="t-branch">└</span>
            <span :class="['t-err-badge', item.errType || 'exec']">{{ errLabel(item.errType) }}</span>
            <span class="t-err-msg">{{ trunc(item.msg, 110) }}</span>
            <span class="t-gap"></span>
            <span class="t-expand">{{ expandedResults['err-' + item.key] ? '−' : '+' }}</span>
          </div>

          <div v-if="item.type === 'error' && expandedResults['err-' + item.key]"
               class="tl-result failed" :style="indent(item.depth + 1)">
            <pre class="t-code">{{ item.msg }}</pre>
          </div>

        </template>

        <div class="tl tl-footer">
          <span class="t-rail footer"></span>
          <span class="t-dim">{{ nodes.length }} nodes</span>
          <span class="t-dim">·</span>
          <span class="t-dim">{{ totalMs }}ms</span>
          <span v-if="totalTokens > 0" class="t-dim">·</span>
          <span v-if="totalTokens > 0" class="t-dim">{{ fmtTokens(totalTokens) }} tokens ({{ fmtTokens(totalTokensIn) }}in · {{ fmtTokens(totalTokensOut) }}out)</span>
          <span class="t-dim">·</span>
          <span :class="['t-final', status]">{{ statusLabel }}</span>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup>
/**
 * desc: DAG execution trace visualizer that renders nodes, dependencies, batches, and errors in a terminal-style layout
 */
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  running: { type: Boolean, default: false },
})

/*
 * resultText is a node's result with anything the payload above it already
 * shows taken out.
 * desc: A node's result is its Evidence — the text the MODEL reads. For a tool
 *       that failed, Evidence is deliberately the failure line followed by the
 *       tool's data serialised, so a model reading about a failure still gets
 *       what the tool managed to produce.
 *
 *       The trace shows those same fields directly above, pretty-printed and
 *       coloured, out of node.payload. Rendering both put one JSON object on
 *       screen twice — the second time as an escaped single-line blob nobody
 *       can read.
 *
 *       So when a payload is shown, only the prose survives here. Evidence puts
 *       its data last, so everything from the first line opening a JSON value
 *       onward is the copy.
 * param: node - the trace node.
 * return: what is left to show, or '' when the payload said all of it.
 */
function resultText (node) {
  if (!node.result) return ''
  if (!node.payload) return node.result
  // Evidence puts its data last, so everything from the first line opening a
  // JSON value onward is the copy.
  const lines = node.result.split('\n')
  const data = lines.findIndex(l => /^\s*[[{]/.test(l))
  const prose = (data === -1 ? lines : lines.slice(0, data)).join('\n').trim()
  if (!prose) return ''
  // A tool that SUCCEEDED has Evidence that is its content field, so the same
  // text would show as prose here and as a field above. Neither copy is whole:
  // the result is cut at 512 characters and the payload shortens every value at
  // 400, and the result can carry a trailing note the field does not. So they
  // are matched on the opening they share rather than compared outright.
  const content = node.payload.content
  if (typeof content === 'string') {
    const trim = t => t.replace(/…+$/, '').trim()
    const a = trim(prose), b = trim(content)
    const n = Math.min(a.length, b.length, 200)
    if (n >= 40 && a.slice(0, n) === b.slice(0, n)) return ''
  }
  return prose
}

const expanded = ref(false)
const expandedResults = ref({})
// The trace stays as the reader left it.
//
// It used to collapse itself 2.5 seconds after a run finished — which is the
// moment there is most to read, and it took the whole thing away mid-sentence.
// Whether it is open is the reader's decision, and a run ending is not a reason
// to reverse it.
watch(() => props.nodes.length, (n, o) => { if (n > 0 && o === 0) expanded.value = false })

// ── Live activity ticker ──────────────────────────────────────────────────────
// A local clock that runs only while the DAG is live. It drives a braille spinner
// and an elapsed counter so the header visibly MOVES even during a silent step,
// and it re-evaluates `activity` (the current running node) on every frame.
const now = ref(0)
const runStart = ref(0)
let ticker = null
const SPIN = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']

function startTicker() {
  runStart.value = performance.now()
  now.value = runStart.value
  if (ticker) clearInterval(ticker)
  ticker = setInterval(() => { now.value = performance.now() }, 120)
}
function stopTicker() { if (ticker) { clearInterval(ticker); ticker = null } }

watch(() => props.running, (r) => { r ? startTicker() : stopTicker() })
onMounted(() => { if (props.running) startTicker() })
onUnmounted(stopTicker)

const spinner = computed(() => SPIN[Math.floor(now.value / 120) % SPIN.length])

const liveElapsed = computed(() => {
  const s = Math.max(0, (now.value - runStart.value) / 1000)
  if (s < 60) return s.toFixed(1) + 's'
  const m = Math.floor(s / 60)
  return `${m}m ${String(Math.floor(s % 60)).padStart(2, '0')}s`
})

// Plain-English verb for the type of the step currently doing the work.
const ACT_VERB = {
  aggregator: 'synthesizing answer', reflection: 'reflecting', observer: 'observing',
  micro_planner: 'planning sub-steps', compute: 'computing', interjection: 'handling your note',
  holmes: 'reasoning about root cause', actuator: 'acting',
}

// What the run is doing RIGHT NOW — the running node(s), named the way a person
// would say it, with the tool's target (url/path/query) when there is one.
const activity = computed(() => {
  const live = props.nodes.filter(n => n.state === 'running')
  if (!live.length) return 'working'
  if (live.length > 1) return `${live.length} steps running`
  const n = live[0]
  if (n.type === 'chat') return 'answering'
  if (n.type === 'executive') return n.tag === 'replan' ? 're-planning' : 'planning'
  if (n.type === 'tool') {
    const target = firstParam(n.params)
    const name = n.tool || n.tag || 'tool'
    return target ? `${name} → ${target}` : name
  }
  return ACT_VERB[n.type] || n.tag || n.tool || 'working'
})

/** Pull the most meaningful single param value (url/path/query/…) for display. */
function firstParam(p) {
  if (!p) return ''
  try {
    const obj = typeof p === 'string' ? JSON.parse(p) : p
    for (const k of ['url', 'path', 'query', 'command', 'q', 'name']) {
      if (obj[k]) return shortTarget(String(obj[k]))
    }
    const v = Object.values(obj)[0]
    return v != null ? shortTarget(String(v)) : ''
  } catch { return '' }
}
function shortTarget(s) {
  s = s.replace(/^https?:\/\//, '')
  return s.length > 32 ? s.slice(0, 32) + '…' : s
}

/**
 * desc: Toggle the expanded/collapsed state of a node's result content
 * @param {string} id - The node ID whose result visibility to toggle
 * @returns {void}
 */
function toggleResult(id) {
  expandedResults.value[id] = !expandedResults.value[id]
}

/**
 * desc: Compute the overall trace status based on node states and running flag
 * @returns {string} One of 'live', 'fail', 'done', or 'idle'
 */
const status = computed(() => {
  if (props.running) return 'live'
  const failed = failCount.value
  const total = props.nodes.length
  if (failed > 0 && failed < total) return 'partial'
  if (failed > 0) return 'fail'
  if (total && props.nodes.every(n => n.state === 'resolved' || n.state === 'skipped')) return 'done'
  return 'idle'
})

const statusLabel = computed(() => {
  if (status.value === 'live') return 'live'
  if (status.value === 'done') return 'done'
  if (status.value === 'partial') {
    const passed = props.nodes.filter(n => n.state === 'resolved').length
    return `${passed} ok · ${failCount.value} failed`
  }
  if (status.value === 'fail') return `${failCount.value} failed`
  return 'idle'
})

/**
 * desc: Compute the total execution time in milliseconds across all nodes
 * @returns {number} Sum of all node ms values
 */
const totalMs = computed(() => props.nodes.reduce((s, n) => s + (n.ms || 0), 0))

/**
 * desc: Compute the number of failed nodes in the trace
 * @returns {number} Count of nodes with state 'failed'
 */
const failCount = computed(() => props.nodes.filter(n => n.state === 'failed').length)

const totalTokensIn = computed(() => props.nodes.reduce((s, n) => s + (n.tokens_in || 0), 0))
const totalTokensOut = computed(() => props.nodes.reduce((s, n) => s + (n.tokens_out || 0), 0))
const totalTokens = computed(() => totalTokensIn.value + totalTokensOut.value)

const latestTag = computed(() => {
  const active = props.nodes.filter(n => n.state === 'running' || n.state === 'resolved')
  if (active.length === 0) return ''
  const last = active[active.length - 1]
  return last.tag || last.tool || last.id
})

/**
 * desc: Build the flat layout array from nodes, grouping independent tools into batches and attaching deps/spawns/errors
 * @returns {Array<Object>} Layout items for rendering (node, batch-open, batch-close, dep, spawn, error)
 */
const layout = computed(() => {
  // The order they arrived in, which is the order they ran. Both writers hand
  // it over that way already: the live stream appends each node as its event
  // lands, and a reloaded trace is the graph's own insertion order.
  //
  // This was sorted — every `executive` pinned to the front, the aggregator to
  // the back, then by the digits in the node id. Two things went wrong with
  // that. Planning nodes are not one bookend but one per replan, so a run with
  // three replans showed four planning rows stacked at the top and every tool
  // below them, which reads as four plans that ran back to back. And the ids do
  // not share a numbering: a tool is n1, n2, n3 while a replan is
  // executive-r1, so the digits collide and the tie is decided by whatever
  // order the array happened to be in.
  const nodes = [...props.nodes]

  // Depth is the length of the longest chain of dependencies behind a node, so
  // indentation follows the plan's own wiring rather than the order steps
  // happened to be created in. Two steps that run in parallel share a depth and
  // line up; a step that had to wait for another sits under it.
  //
  // This replaces the 00/01/02 gutter. A number told a reader where a step came
  // in a list, which is the one thing about a DAG that does not matter.
  const byId = new Map(nodes.map(n => [n.id, n]))
  const depthOf = new Map()
  const depth = (n, seen = new Set()) => {
    if (depthOf.has(n.id)) return depthOf.get(n.id)
    if (seen.has(n.id)) return 0            // a cycle cannot indent forever
    seen.add(n.id)
    const parents = [...(n.deps || []), ...(n.spawn ? [n.spawn] : [])]
      .map(id => byId.get(id)).filter(Boolean)
    const d = parents.length ? Math.max(...parents.map(p => depth(p, seen))) + 1 : 0
    depthOf.set(n.id, d)
    return d
  }
  nodes.forEach(n => depth(n))

  // Bookends sit outside the plan, at the root.
  nodes.forEach(n => {
    if (n.type === 'executive' || n.type === 'aggregator') depthOf.set(n.id, 0)
  })

  const items = []
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i]
    const d = depthOf.get(n.id) || 0
    // The last sibling at this depth closes its branch with an elbow rather
    // than a tee, which is what makes a list of rows read as a tree.
    let last = true
    for (let j = i + 1; j < nodes.length; j++) {
      const dj = depthOf.get(nodes[j].id) || 0
      if (dj < d) break
      if (dj === d) { last = false; break }
    }
    pushNode(items, n, d, last)
  }
  return items
})

/**
 * desc: Append a node and its sub-rows (tools, skills, spawn, error) to the layout
 * @param {Array<Object>} items - The layout items array to push onto
 * @param {Object} n - The DAG node object
 * @param {number} depth - How many dependency hops sit behind this node
 * @param {boolean} last - Whether this is the last node at its depth
 * @returns {void}
 */
function pushNode(items, n, depth, last) {
  items.push({ type: 'node', key: `n-${n.id}`, node: n, depth, last })
  const sub = depth + 1
  // What the planner was shown, and the text it was ranked against. Only
  // planning nodes carry these — see NodeInfo.Tools.
  if (n.tools && n.tools.length) {
    items.push({ type: 'tools', key: `tl-${n.id}`, node: n, depth: sub })
  }
  // Interjection nodes show the operator's original query directly under the
  // node, so it reads as "you asked X → decided Y" instead of the query
  // blurring into the reflection decision.
  if (n.type === 'interjection' && n.operator_message) {
    items.push({ type: 'interject', key: `ij-${n.id}`, msg: n.operator_message, depth: sub })
  }
  if (n.skills && n.skills.length) {
    items.push({ type: 'skills', key: `sk-${n.id}`, skills: n.skills, depth: sub })
  }
  // The dependency row is gone: indentation says what it said, without a line
  // per edge. A step that was spawned rather than planned still says so,
  // because that is not visible from the shape.
  if (n.spawn) {
    const sn = props.nodes.find(x => x.id === n.spawn)
    if (sn) items.push({ type: 'spawn', key: `s-${n.id}`, name: sn.tool || sn.tag || sn.id, depth: sub })
  }
  if (n.err) {
    items.push({ type: 'error', key: `e-${n.id}`, msg: n.err, errType: n.err_type, depth: sub })
  }
}

/**
 * desc: Format node parameters into a compact key=value string for inline display
 * @param {string|Object} p - Parameters as a JSON string or object
 * @returns {string} Compact formatted parameter string
 */
function compactParams(p) {
  if (!p) return ''
  try {
    const obj = typeof p === 'string' ? JSON.parse(p) : p
    return Object.entries(obj).map(([k, v]) => {
      let val = typeof v === 'string' ? v : JSON.stringify(v)
      if (val.length > 18) val = val.slice(0, 18) + '\u2026'
      return `${k}=${val}`
    }).join('  ')
  } catch { return '' }
}

/**
 * desc: Format a byte count into a human-readable size string (b or kb)
 * @param {number} b - Size in bytes
 * @returns {string} Formatted size string
 */
function fmtSize(b) { return b < 1024 ? b + 'b' : (b / 1024).toFixed(1) + 'kb' }

// The payload arrives already shortened per value by the backend, so this only
// lays it out. Anything unparseable is shown as it came rather than dropped,
// because a reader looking for a field needs to see there was one.
function fmtPayload(p) {
  if (p == null) return ''
  try {
    return JSON.stringify(typeof p === 'string' ? JSON.parse(p) : p, null, 2)
  } catch (e) {
    return typeof p === 'string' ? p : String(p)
  }
}
function fmtTokens(t) { return t >= 1000 ? (t / 1000).toFixed(1) + 'k' : String(t) }

/**
 * desc: Map an error type to its display label with icon
 * @param {string} t - Error type (gate, clearance, timeout, or other)
 * @returns {string} Formatted error label
 */
function errLabel(t) { return { gate: '\u26D4 GATE', clearance: '\uD83D\uDD12 CLEARANCE', timeout: '\u23F1 TIMEOUT' }[t] || '\u2717 ERROR' }

/**
 * desc: Indentation for a row at a given depth in the dependency tree
 * @param {number} d - Depth, in dependency hops
 * @returns {Object} A style binding
 */
function indent(d) { return { paddingLeft: (d || 0) * 13 + 'px' } }

// ── JSON, coloured ────────────────────────────────────────────────────────────
// One pass over the text, splitting it into the five things worth telling apart:
// a key, a string, a number, a literal, and the punctuation between them. Not a
// highlighter — there is no language to parse, no grammar, and nothing to load.
// Text that is not JSON produces one plain part and renders as it always did.
const JSON_TOKEN = /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}\[\],])/g

/**
 * desc: Split text into coloured JSON parts for display
 * @param {string} text - The text to split
 * @returns {Array<{c: string, t: string}>} Parts, each with a class and its text
 */
function jsonParts(text) {
  if (typeof text !== 'string' || !text) return []
  // Only colour what is actually JSON. A tool returning prose gets left alone
  // rather than having quotes inside a sentence lit up as if they were values.
  const head = text.trimStart()[0]
  if (head !== '{' && head !== '[') return [{ c: 'plain', t: text }]

  const parts = []
  let last = 0
  let m
  JSON_TOKEN.lastIndex = 0
  while ((m = JSON_TOKEN.exec(text)) !== null) {
    if (m.index > last) parts.push({ c: 'plain', t: text.slice(last, m.index) })
    if (m[1] && m[2]) {
      parts.push({ c: 'key', t: m[1] })
      parts.push({ c: 'punc', t: m[2] })
    } else if (m[1]) parts.push({ c: 'str', t: m[1] })
    else if (m[3]) parts.push({ c: 'num', t: m[3] })
    else if (m[4]) parts.push({ c: 'lit', t: m[4] })
    else if (m[5]) parts.push({ c: 'punc', t: m[5] })
    last = JSON_TOKEN.lastIndex
  }
  if (last < text.length) parts.push({ c: 'plain', t: text.slice(last) })
  return parts
}

/**
 * desc: Map a node state to its single-character status symbol
 * @param {string} s - Node state (running, resolved, failed, skipped)
 * @returns {string} Status character
 */
function stChr(s) { return { running: '\u25B8', resolved: '\u2713', failed: '\u2717', skipped: '\u2013' }[s] || '\u00B7' }

/**
 * desc: Map a node type to its three-letter abbreviation label
 * @param {string} t - Node type (planner, aggregator, skill, etc.)
 * @returns {string} Three-letter type label
 */
// Every type NodeType.String() can return. A missing one renders as '???',
// which is what the chat lane's own node did — it is a real node with a real
// answer in it, labelled as though the trace did not know what it was.
const TY_LABEL = {
  executive: 'EXE', aggregator: 'AGG', tool: 'TOOL', compute: 'CMP',
  reflection: 'RFL', observer: 'OBS', micro_planner: 'MPL',
  interjection: 'INJ', actuator: 'ACT', holmes: '🕵️', chat: 'CHAT',
}
function tyLabel (t) { return TY_LABEL[t] || (t ? t.slice(0, 4).toUpperCase() : '???') }

/**
 * desc: Truncate a string to a maximum length, appending an ellipsis if needed
 * @param {string} s - The string to truncate
 * @param {number} n - Maximum length before truncation
 * @returns {string} Truncated string
 */
function trunc(s, n) { return s && s.length > n ? s.slice(0, n) + '\u2026' : s }

function parseReasoning(result) {
  try {
    const obj = typeof result === 'string' ? JSON.parse(result) : result
    return obj.reasoning || null
  } catch { return null }
}

function parseRCA(result) {
  try {
    const obj = typeof result === 'string' ? JSON.parse(result) : result
    return obj.rca?.root_cause || null
  } catch { return null }
}
</script>

<style scoped>
/* ── Palette ───────────────────────────────────────────────────────────────
   One hue per kind of step, bright enough to tell apart at a glance in a dense
   list. Cool at the centre — cyan, blue, violet — with amber and pink as the
   warm exceptions so a human interjection and a slow reflection stand out
   against the machinery. Tool stays the quiet one: most rows are tools, and if
   every row shouts none of them does. */
.trace {
  --n-plan:    #0891b2;   /* executive, aggregator — cyan */
  --n-think:   #d97706;   /* reflection, observer, micro-planner — amber */
  --n-tool:    #64748b;   /* tool — slate */
  --n-compute: #2563eb;   /* compute — blue */
  --n-rca:     #7c3aed;   /* holmes — violet */
  --n-human:   #db2777;   /* interjection — pink */
  --n-danger:  #dc2626;   /* failures, actuator — red */
  --n-ok:      #059669;   /* resolved — emerald */
  --n-on-danger: #fff;

  /* Two hues, not one. The wash travels cyan → blue across the card, and that
     shift IS the effect — the same colour at two alphas only fades, which is
     what flattening this to a single hue cost it. The green cast that needed
     fixing was the lead chip below, not this. */
  --card-bg:     rgba(8, 145, 178, 0.05);
  --card-tint:   rgba(37, 99, 235, 0.07);
  --card-edge:   rgba(8, 145, 178, 0.22);
  --card-top:    rgba(255, 255, 255, 0.55);
  --card-shadow: 0 1px 1px rgba(15, 23, 42, 0.05), 0 10px 24px -16px rgba(15, 23, 42, 0.35);
  --card-sweep:  rgba(8, 145, 178, 0.10);

  /* The chip marking the tool the planner led with. The sage green below is
     what dark uses, and on near-black under cyan text it glows. On white under
     the same cyan it is just muddy, so light takes the plan hue instead and the
     chip agrees with its own text. */
  --chip-lead-bg:   rgba(8, 145, 178, 0.10);
  --chip-lead-edge: rgba(8, 145, 178, 0.42);

  /* The skill cards a step was guided by. Dusty mauve, hardcoded for both
     themes, read as grey on either. A violet that belongs to the theme. */
  --chip-skill-bg:   rgba(147, 51, 234, 0.09);
  --chip-skill-fg:   #7e22ce;
  --chip-skill-edge: rgba(147, 51, 234, 0.30);
}
[data-theme="dark"] .trace {
  /* Brighter on dark, and pushed toward the ends of the spectrum rather than
     the middle: on near-black a desaturated hue reads as grey, so the colours
     that separate one kind of step from another have to be lit to separate at
     all. Tool stays the quiet one — most rows are tools. */
  --n-plan:    #22d3ee;
  --n-think:   #ffc857;
  --n-tool:    #8fa3bf;
  --n-compute: #7cc4ff;
  --n-rca:     #c084fc;
  --n-human:   #ff77c8;
  --n-danger:  #ff6b6b;
  --n-ok:      #4ade80;
  --n-on-danger: #1a0b0b;

  --card-bg:     rgba(34, 211, 238, 0.07);
  --card-tint:   rgba(96, 165, 250, 0.08);
  --card-edge:   rgba(34, 211, 238, 0.26);
  --card-top:    rgba(255, 255, 255, 0.10);
  --card-shadow: 0 1px 1px rgba(0, 0, 0, 0.30), 0 12px 28px -18px rgba(0, 0, 0, 0.8);
  --card-sweep:  rgba(34, 211, 238, 0.13);

  /* Unchanged: this green is what dark looked like, and it works there. */
  --chip-lead-bg:   rgba(127, 156, 151, 0.12);
  --chip-lead-edge: rgba(127, 156, 151, 0.55);

  /* Lit, not tinted. On near-black the old mauve was indistinguishable from the
     grey rows around it. */
  --chip-skill-bg:   rgba(232, 121, 249, 0.13);
  --chip-skill-fg:   #f0abfc;
  --chip-skill-edge: rgba(232, 121, 249, 0.42);
}

.trace {
  position: relative;
  margin: 10px 0 0;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.2;
  padding: 9px 12px 10px;
  /* Softened corners, not a card. The wash ends in a straight cut otherwise,
     which is the one part of the border treatment worth keeping. */
  border-radius: 10px;
  /* The wash and nothing else. A border, a radius and a shadow made this a card
     sitting ON the conversation; the trace is part of the reply, not an object
     beside it. The gradient alone still separates it — brightest at the top-left
     where the reading starts, gone by two-thirds, so nothing lines up with the
     ragged right edge of the rows. */
  background-image:
    linear-gradient(105deg, var(--card-bg) 0%, var(--card-tint) 26%, transparent 68%);
  background-size: 100% 100%;
  background-repeat: no-repeat;
}
/* While the run is live a soft band travels across the wash — the signal the
   separate scan line used to carry, moved onto the thing it was describing.
   Low contrast on purpose: it passes UNDER eleven rows of text that have to stay
   readable while it moves, so it is a lightening rather than a highlight.
   Settled, the trace keeps only the static wash. */
.trace.live {
  background-image:
    linear-gradient(100deg, transparent 0%, var(--card-sweep) 50%, transparent 100%),
    linear-gradient(105deg, var(--card-bg) 0%, var(--card-tint) 26%, transparent 68%);
  background-size: 55% 100%, 100% 100%;
  background-repeat: no-repeat, no-repeat;
  animation: trace-sweep 3.2s ease-in-out infinite;
}
@keyframes trace-sweep {
  0%   { background-position: -60% 0, 0 0; }
  100% { background-position: 170% 0, 0 0; }
}
@media (prefers-reduced-motion: reduce) {
  .trace.live { animation: none; }
}

.trace-header {
  position: relative;
  display: flex; align-items: center; gap: 6px;
  cursor: pointer; user-select: none;
  color: var(--text-muted); transition: color var(--transition);
}
.trace-header:hover { color: var(--text-secondary); }
.h-chr { color: var(--text-muted); }
.h-title { font-weight: 700; text-transform: uppercase; letter-spacing: 0.1em; color: var(--text-secondary); font-size: 10px; }
.h-sep { color: var(--border); }
.h-val { color: var(--text); font-weight: 600; }
.h-dim { color: var(--text-muted); }
.h-latest { color: var(--text-secondary); font-size: 10px; margin-left: 4px; max-width: 200px; overflow: hidden; text-overflow: ellipsis; opacity: 0.7; }
.h-status { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; }

/* Live ticker — spinner + current-step text + elapsed clock, shown while running. */
.h-spin { color: var(--accent); font-weight: 700; width: 10px; display: inline-block; text-align: center; }
.h-activity {
  color: var(--accent); font-size: 10px; font-weight: 600; letter-spacing: 0.02em;
  max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  animation: activity-pulse 1.6s ease-in-out infinite;
}
@keyframes activity-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
.h-elapsed { color: var(--text-muted); font-size: 10px; font-variant-numeric: tabular-nums; }

.h-status.live { color: var(--accent); }
.h-status.done { color: var(--signal-green); }
.h-status.partial { color: var(--signal-amber); }
.h-status.fail { color: var(--signal-red); }

.trace-body { position: relative; padding: 4px 0; }

.tl { display: flex; align-items: center; gap: 5px; padding: 2px 0; white-space: nowrap; }
.tl-sub { padding: 1px 0; opacity: 0.92; }
.tl-node { border-radius: 3px; }

/* ── Tree ──────────────────────────────────────────────────────────────────
   Indentation follows the plan's dependency edges, so what ran in parallel
   lines up and what had to wait sits under what it waited for. The elbow says
   which row closes a branch; the rail carries the node's type as colour, which
   is the same signal the expanded block below it uses. */
.t-branch { color: var(--border); flex-shrink: 0; width: 9px; opacity: 0.75; }
.t-rail {
  flex-shrink: 0; width: 2px; align-self: stretch; min-height: 12px;
  border-radius: 1px; background: var(--border); margin-right: 3px;
}
.t-rail.executive, .t-rail.aggregator, .t-rail.chat { background: var(--n-plan); }
.t-rail.reflection, .t-rail.observer, .t-rail.micro_planner { background: var(--n-think); }
.t-rail.tool { background: var(--n-tool); }
.t-rail.compute { background: var(--n-compute); }
.t-rail.holmes { background: var(--n-rca); }
.t-rail.interjection { background: var(--n-human); }
.t-rail.actuator { background: var(--n-danger); }
.t-rail.failed { background: var(--n-danger); width: 3px; }
.t-rail.running { animation: rail-pulse 1.2s ease-in-out infinite; }
.t-rail.footer { background: var(--border); }
@keyframes rail-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }

/* Pushes everything after it to the right edge, so timings and status form a
   column whatever the indentation does to the left of them. */
.t-gap { flex: 1 1 auto; min-width: 8px; }

/* Type — now before name */
.t-ty { width: 34px; flex-shrink: 0; font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
.t-ty.executive, .t-ty.aggregator, .t-ty.chat { color: var(--n-plan); }
.t-ty.reflection, .t-ty.observer, .t-ty.micro_planner { color: var(--n-think); }
.t-ty.tool { color: var(--n-tool); }
.t-ty.compute { color: var(--n-compute); }
.t-ty.holmes { color: var(--n-rca); }
.t-ty.is-skill { color: var(--chip-skill-fg); }
.t-name.is-skill { color: var(--chip-skill-fg); font-style: italic; }
.t-ty.interjection { color: var(--n-human); }
.t-ty.actuator { color: var(--n-danger); }

.t-name { color: var(--text); font-weight: 500; }
/* A step that was retried. It used to be spelled into the step's name —
   "clone_repo [twotime_retry]" — which renamed the step mid-run and made it
   unaddressable by a reference. The fact belongs beside the name, not in it. */
.t-retry {
  font-size: 9px; font-weight: 600; letter-spacing: 0.04em;
  padding: 1px 5px; border-radius: 3px; flex-shrink: 0;
  color: var(--n-think);
  background: color-mix(in srgb, var(--n-think) 15%, transparent);
}

.t-params { color: var(--text-muted); font-size: 10px; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
.t-summary { color: var(--text-secondary); font-size: 10px; font-style: italic; max-width: 300px; overflow: hidden; text-overflow: ellipsis; }
.t-ms { color: var(--text-muted); font-size: 10px; }
.t-tokens { color: var(--n-plan); font-size: 9px; opacity: 0.65; }
.t-size { color: var(--text-muted); font-size: 9px; opacity: 0.6; }
.t-expand { color: var(--n-plan); font-weight: 700; font-size: 10px; opacity: 0.6; cursor: pointer; }
.t-expand:hover { opacity: 1; }

/* State — at the end now */
.t-st { width: 10px; text-align: center; font-weight: 700; flex-shrink: 0; }
.t-st.running { color: var(--n-plan); animation: blink-st 1s step-end infinite; }
.t-st.resolved { color: var(--n-ok); }
.t-st.failed { color: var(--n-danger); font-size: 12px; }
.t-st.skipped, .t-st.pending { color: var(--n-tool); opacity: 0.7; }
@keyframes blink-st { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

/* A step nobody planned says so. Everything else that used to be written as a
   dependency line is now said by the indentation. */
.t-spawn { color: var(--n-think); font-size: 10px; }

/* Interjection query line — amber to match the INJ node type, so "you asked …"
   visually belongs to the interjection it sits under, distinct from the
   reflection decision the node renders as its summary. */
.t-interject-label { color: var(--n-human); font-size: 10px; font-weight: 600; font-style: italic; }
.t-interject-msg { color: var(--text-secondary); font-size: 10px; max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.t-sub-label { color: var(--text-dim); font-size: 10px; font-style: italic; }
.t-sub-val { color: var(--text); font-weight: 600; font-size: 10px; font-variant-numeric: tabular-nums; }
.t-tool-peek { color: var(--text-muted); font-size: 10px; opacity: 0.7; max-width: 300px; overflow: hidden; text-overflow: ellipsis; }

/* The tools a planning step was shown, in the order it was shown them. The
   first is marked because it leads for a reason — the shell is pinned there,
   and after it the order is what the search made of the objective below. */
.t-chips { display: flex; flex-wrap: wrap; gap: 3px; }
.t-tool-chip {
  font-size: 9px; font-weight: 600; letter-spacing: 0.02em;
  padding: 1px 5px; border-radius: 3px;
  background: var(--surface-alt, rgba(127,127,127,0.10));
  color: var(--text-secondary);
  border: 1px solid var(--border, rgba(127,127,127,0.25));
}
.t-tool-chip.lead {
  color: var(--n-plan);
  border-color: var(--chip-lead-edge);
  background: var(--chip-lead-bg);
}
.t-objective {
  margin-top: 6px; padding-top: 5px;
  border-top: 1px solid var(--border-subtle, rgba(127,127,127,0.2));
  white-space: normal;
}
.t-objective-label {
  color: var(--text-dim); font-size: 9px; font-style: italic;
  text-transform: uppercase; letter-spacing: 0.06em; margin-right: 6px;
}
.t-objective-text { color: var(--text-secondary); font-size: 10px; line-height: 1.45; }
.t-skill-chip {
  display: inline-block;
  font-size: 9px;
  font-weight: 600;
  letter-spacing: 0.03em;
  padding: 1px 6px;
  margin-left: 4px;
  border-radius: 3px;
  background: var(--chip-skill-bg);
  color: var(--chip-skill-fg);
  border: 1px solid var(--chip-skill-edge);
}

/* Errors — dark pink (#c0428a) so failures are sharper than the soft
   light-pink signal-red they used to share with informational warnings.
   Hardcoded so the contrast holds in both light and dark themes. */
/* Errors. Filled, not tinted — a faint colour on a faint wash at nine pixels
   was a failure you had to already know about to find. The badge carries the
   kind, the message runs at body size, and the whole row sits on a ground so
   it separates from the steps that worked. */
.tl-err {
  background: color-mix(in srgb, var(--n-danger) 12%, transparent);
  border-radius: 3px;
  padding: 3px 6px 3px 0;
}
.t-err-badge {
  font-size: 9.5px; font-weight: 700; letter-spacing: 0.06em;
  padding: 2px 6px; border-radius: 3px; flex-shrink: 0;
  background: var(--n-danger); color: var(--n-on-danger);
}
.t-err-badge.clearance { background: var(--n-human); }
.t-err-badge.timeout { background: var(--n-think); }
.t-err-msg {
  color: var(--n-danger); font-size: 11.5px; font-weight: 500;
  overflow: hidden; text-overflow: ellipsis;
}
.tl-err .t-expand { color: var(--n-danger); opacity: 1; }

/* Footer */
.tl-footer { margin-top: 3px; }
.t-dim { color: var(--text-muted); }
.t-final { font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; font-size: 10px; }
.t-final.done { color: var(--signal-green); }
.t-final.partial { color: var(--signal-amber); }
.t-final.fail { color: var(--signal-red); }
.t-final.live { color: var(--accent); }

/* Clickable node rows */
.tl-clickable { cursor: pointer; border-radius: 2px; transition: background var(--transition); }
.tl-clickable:hover { background: var(--surface-hover, rgba(128,128,128,0.08)); }

/* Expanded result block — purple left border so the expanded payload
   visually belongs to the dep/compute family rather than the accent. */
/* The expanded block wears the colour of the node it came from, so an open
   payload is visibly attached to its step rather than floating under it. */
.tl-result {
  margin: 1px 0 5px 0;
  padding: 6px 9px;
  background: var(--surface-alt, rgba(0,0,0,0.04));
  border-left: 2px solid var(--border);
  border-radius: 0 3px 3px 0;
  max-height: 220px;
  overflow: auto;
}
.tl-result.executive, .tl-result.aggregator { border-left-color: var(--n-plan); }
.tl-result.reflection, .tl-result.observer, .tl-result.micro_planner { border-left-color: var(--n-think); }
.tl-result.compute { border-left-color: var(--n-compute); }
.tl-result.holmes { border-left-color: var(--n-rca); }
.tl-result.tool { border-left-color: var(--n-tool); }
.tl-result.failed {
  border-left-color: var(--n-danger);
  border-left-width: 3px;
  background: color-mix(in srgb, var(--n-danger) 8%, var(--surface-alt, rgba(0,0,0,0.04)));
}
.tl-result.failed .t-code { color: var(--text); font-size: 11.5px; }
.t-code {
  margin: 0;
  font-size: 10px;
  line-height: 1.45;
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

/* JSON, told apart. Five colours and no more: a key, a string, a number, a
   literal, and the punctuation holding them together. Anything that is not
   JSON renders as one plain part and looks exactly as it did before. */
.j-key  { color: var(--n-plan); font-weight: 600; }
.j-str  { color: #9aa87e; }
.j-num  { color: var(--n-compute); }
.j-lit  { color: var(--n-rca); font-weight: 600; }
.j-punc { color: var(--n-tool); opacity: 0.55; }
.j-plain { color: var(--text-secondary); }

/* The tool's own fields, set apart from the text below them so a reader can
   tell what the tool returned from what it rendered. */
.t-payload {
  border-left: 1px solid var(--border, rgba(127, 127, 127, 0.3));
  padding-left: 8px;
  margin-bottom: 6px;
}

/* RCA reasoning display */
.t-rca-content {
  font-family: var(--font);
  font-size: 11px;
  line-height: 1.5;
  color: var(--text-secondary);
}
.t-rca-reasoning {
  margin-bottom: 6px;
  font-style: italic;
}
.t-rca-conclusion {
  padding-top: 4px;
  border-top: 1px solid var(--border-subtle);
  color: var(--text);
  font-weight: 500;
}
.t-rca-label {
  color: var(--n-rca);
  font-weight: 700;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

/* Opacity and a small lift, never height.
   This used to animate max-height between 0 and 1000px. A trace longer than
   1000px was cut off at that line, and a run of any size reaches it — so the
   bottom of a long trace was simply not there. Nothing here constrains height,
   so nothing can clip. */
.expand-enter-active { transition: opacity 0.2s ease, transform 0.2s ease; }
.expand-leave-active { transition: opacity 0.12s ease, transform 0.12s ease; }
.expand-enter-from, .expand-leave-to { opacity: 0; transform: translateY(-3px); }
.expand-enter-to, .expand-leave-from { opacity: 1; transform: none; }
</style>
